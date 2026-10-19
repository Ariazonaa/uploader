// Minimal tus 1.0 client: chunked, resumable uploads against the backend's
// /api/v1/tus endpoints (core + creation + termination).
//
// - A dropped connection, a 429 or a 5xx is retried with backoff (honoring
//   Retry-After); before each retry the offset is resynced via HEAD, so the
//   transfer resumes where the server actually is instead of restarting.
// - The final step is retried the same way: the server keeps a fully received
//   upload after a transient finalize error, and HEAD reports the result if the
//   finalize succeeded but its response was lost.
// - On final failure or cancel the upload is terminated (DELETE) so it stops
//   counting against the server's per-account cap of unfinished uploads. So
//   is every unfinished upload when the page goes away (reload, tab closed):
//   the URLs only live in memory, so nothing could resume them afterwards.

import { tGlobal, translateApiError } from "./i18n";
import { abortError, backoffMs, isAbortError, retryAfterMs, sleep } from "./retry";

const CHUNK = 5 * 1024 * 1024; // 5 MiB
// Consecutive failed requests tolerated before an upload is given up.
const MAX_FAILURES = 6;
const TUS_HEADERS = { "Tus-Resumable": "1.0.0" };

export interface TusProgress {
  pct: number;
  speed: number; // bytes/sec
  eta: number; // seconds remaining
}

export interface TusOptions {
  // Store the blob encrypted at rest; undefined leaves it to the server default.
  encrypt?: boolean;
  // Aborting cancels the upload and terminates it on the server.
  signal?: AbortSignal;
}

// Upload URLs are remembered for the page session, but only those of uploads
// that failed and whose terminating DELETE failed too (e.g. the network is
// down): dropping the same file again then resumes it instead of opening a new
// upload. Never while a transfer runs - a second upload of the same file would
// share it, and cancelling either would terminate both.
const knownUploads = new Map<string, string>();

// Upload URLs with a transfer running right now.
const activeUploads = new Set<string>();

// On pagehide every unfinished upload this page knows about is abandoned with a
// keepalive DELETE, which the browser still delivers after the page is gone.
// Without it each reload in the middle of a big upload would leave one upload
// on the server until it goes stale, and a few reloads fill the per-account
// cap (429 for up to an hour).
let unloadHookInstalled = false;
function installUnloadHook() {
  if (unloadHookInstalled || typeof window === "undefined") return;
  unloadHookInstalled = true;
  window.addEventListener("pagehide", () => {
    for (const url of new Set([...activeUploads, ...knownUploads.values()])) {
      try {
        void fetch(url, {
          method: "DELETE",
          credentials: "include",
          headers: TUS_HEADERS,
          keepalive: true,
        }).catch(() => {});
      } catch {
        /* the page is going away; nothing left to do */
      }
    }
    activeUploads.clear();
    knownUploads.clear();
  });
}

function fingerprint(file: File, encrypt?: boolean): string {
  return [file.name, file.size, file.lastModified, String(encrypt)].join("|");
}

// relativePath keeps requests same-origin (so the session cookie is sent) even
// if the server returns an absolute Location on a configured public URL.
function relativePath(location: string): string {
  try {
    return new URL(location, window.location.origin).pathname;
  } catch {
    return location;
  }
}

// retryable reports whether a failed response is worth retrying unchanged.
function retryable(status: number): boolean {
  return status === 429 || status === 423 || status >= 500;
}

async function errorFrom(res: Response): Promise<Error> {
  if (res.status === 413) return new Error(tGlobal("errors.fileTooLarge"));
  let msg = tGlobal("uploader.uploadFailed", { status: res.status });
  try {
    const body = await res.json();
    if (body?.error) msg = translateApiError(String(body.error));
  } catch {
    /* no JSON body (e.g. HEAD) — keep the status message */
  }
  return new Error(msg);
}

interface ServerState {
  offset: number;
  length: number;
  url: string | null; // set once the upload has been finalized
}

// head reads the server's view of an upload; null when it no longer exists.
async function head(url: string, signal?: AbortSignal): Promise<ServerState | null> {
  const res = await fetch(url, {
    method: "HEAD",
    credentials: "include",
    cache: "no-store",
    headers: TUS_HEADERS,
    signal,
  });
  if (res.status === 404 || res.status === 410) return null;
  if (!res.ok) throw await errorFrom(res);
  return {
    offset: Number(res.headers.get("Upload-Offset") ?? "0"),
    length: Number(res.headers.get("Upload-Length") ?? "-1"),
    url: res.headers.get("X-Uploader-Url"),
  };
}

// terminate abandons an upload on the server. It reports whether the upload is
// gone (deleted now, or already unknown).
async function terminate(url: string): Promise<boolean> {
  try {
    const res = await fetch(url, { method: "DELETE", credentials: "include", headers: TUS_HEADERS });
    return res.ok || res.status === 404 || res.status === 410;
  } catch {
    return false;
  }
}

// create opens a new upload and returns its (same-origin) URL.
async function create(file: File, opts: TusOptions): Promise<string> {
  const qs = opts.encrypt === undefined ? "" : `?encrypt=${opts.encrypt}`;
  for (let attempt = 0; ; attempt++) {
    let res: Response | null = null;
    try {
      res = await fetch(`/api/v1/tus${qs}`, {
        method: "POST",
        credentials: "include",
        headers: {
          ...TUS_HEADERS,
          "Upload-Length": String(file.size),
          "Upload-Metadata": `filename ${btoa(unescape(encodeURIComponent(file.name)))}`,
        },
        signal: opts.signal,
      });
    } catch (e) {
      if (isAbortError(e)) throw e;
    }
    if (res?.status === 201) {
      const location = res.headers.get("Location");
      if (!location) throw new Error(tGlobal("uploader.invalidResponse"));
      return relativePath(location);
    }
    if (res && !retryable(res.status)) throw await errorFrom(res);
    if (attempt + 1 >= MAX_FAILURES) {
      throw res ? await errorFrom(res) : new Error(tGlobal("uploader.networkError"));
    }
    await sleep(backoffMs(attempt, res ? retryAfterMs(res.headers.get("Retry-After")) : null), opts.signal);
  }
}

// transfer sends the file from `offset` on and returns the share URL.
async function transfer(
  file: File,
  url: string,
  offset: number,
  onProgress: (p: TusProgress) => void,
  signal?: AbortSignal,
): Promise<{ url: string }> {
  const start = Date.now();
  const startOffset = offset;
  let failures = 0;

  for (;;) {
    if (signal?.aborted) throw abortError();
    const end = Math.min(offset + CHUNK, file.size);
    let res: Response | null = null;
    try {
      res = await fetch(url, {
        method: "PATCH",
        credentials: "include",
        headers: {
          ...TUS_HEADERS,
          "Content-Type": "application/offset+octet-stream",
          "Upload-Offset": String(offset),
        },
        // Once every byte is on the server this is empty: a PATCH at offset ==
        // length retries (or re-reads) the final step.
        body: file.slice(offset, end),
        signal,
      });
    } catch (e) {
      if (isAbortError(e)) throw e;
    }

    if (res?.ok) {
      const done = res.headers.get("X-Uploader-Url");
      if (done) return { url: done };
      offset = Number(res.headers.get("Upload-Offset") ?? String(end));
      const elapsed = (Date.now() - start) / 1000;
      const speed = elapsed > 0 ? (offset - startOffset) / elapsed : 0;
      onProgress({
        pct: Math.round((offset / file.size) * 100),
        speed,
        eta: speed > 0 ? (file.size - offset) / speed : 0,
      });
      // Every byte accepted but no result: the server should never answer
      // like that, so treat it as a failure rather than loop forever.
      if (offset < file.size) {
        failures = 0;
        continue;
      }
    } else if (res && res.status !== 409 && !retryable(res.status)) {
      throw await errorFrom(res);
    }

    failures++;
    if (failures > MAX_FAILURES) {
      if (res?.ok) throw new Error(tGlobal("uploader.invalidResponse"));
      throw res ? await errorFrom(res) : new Error(tGlobal("uploader.networkError"));
    }
    // 409 means our offset is stale — resync at once; anything else backs off.
    if (res?.status !== 409) {
      await sleep(
        backoffMs(failures - 1, res && !res.ok ? retryAfterMs(res.headers.get("Retry-After")) : null),
        signal,
      );
    }
    // A failed PATCH may have stored part of its chunk, or a finalize whose
    // response was lost may have completed after all: ask the server.
    let state: ServerState | null;
    try {
      state = await head(url, signal);
    } catch (e) {
      if (isAbortError(e)) throw e;
      continue; // the next PATCH either works or counts as another failure
    }
    if (!state) throw new Error(tGlobal("uploader.uploadFailed", { status: 404 }));
    if (state.url) return { url: state.url };
    // The server kept part of the failed chunk: that is progress, so the
    // upload is not stuck and the failure streak starts over.
    if (state.offset > offset) failures = 0;
    offset = state.offset;
  }
}

export async function tusUpload(
  file: File,
  onProgress: (p: TusProgress) => void,
  opts: TusOptions = {},
): Promise<{ url: string }> {
  installUnloadHook();
  const key = fingerprint(file, opts.encrypt);
  // Claim the entry, so a concurrent upload of the same file opens its own.
  let url = knownUploads.get(key) ?? null;
  knownUploads.delete(key);
  let offset = 0;

  if (url) {
    // Resume a previous attempt at this very file if the server still has it.
    let state: ServerState | null = null;
    try {
      state = await head(url, opts.signal);
    } catch (e) {
      if (isAbortError(e)) {
        await terminate(url);
        throw e;
      }
    }
    if (state?.url) return { url: state.url };
    if (state && state.length === file.size) {
      offset = state.offset;
    } else {
      url = null;
    }
  }
  if (!url) {
    url = await create(file, opts);
  }

  activeUploads.add(url);
  try {
    return await transfer(file, url, offset, onProgress, opts.signal);
  } catch (e) {
    // Give the slot back to the server. Only if that fails too is the URL
    // remembered, so a retry of the same file can pick the transfer up again.
    if (!(await terminate(url)) && !isAbortError(e)) knownUploads.set(key, url);
    throw e;
  } finally {
    activeUploads.delete(url);
  }
}
