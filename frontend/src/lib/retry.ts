// Retry/backoff helpers shared by the direct (XHR) and resumable (tus) upload
// paths, plus a small concurrency limiter so a big folder drop does not fire
// every upload at once.

// Longest single wait we are willing to sit through, even if the server's
// Retry-After asks for more; the caller gives up after a few attempts anyway.
const MAX_WAIT_MS = 60_000;

// retryAfterMs parses a Retry-After header (delta-seconds or an HTTP date).
export function retryAfterMs(header: string | null): number | null {
  if (!header) return null;
  const secs = Number(header);
  if (Number.isFinite(secs)) return Math.max(0, secs * 1000);
  const at = Date.parse(header);
  return Number.isNaN(at) ? null : Math.max(0, at - Date.now());
}

// backoffMs is the wait before retry number `attempt` (0-based): the server's
// Retry-After when it sent one, else exponential backoff (1s, 2s, 4s, … capped)
// with jitter so parallel uploads don't retry in lockstep.
export function backoffMs(attempt: number, retryAfter?: number | null): number {
  if (retryAfter != null) return Math.min(retryAfter, MAX_WAIT_MS);
  const base = Math.min(1000 * 2 ** attempt, 30_000);
  return Math.round(base / 2 + Math.random() * (base / 2));
}

// isAbortError reports whether err comes from an aborted request/wait.
export function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError";
}

export function abortError(): DOMException {
  return new DOMException("Aborted", "AbortError");
}

// sleep waits ms, rejecting with an AbortError as soon as signal fires.
export function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(abortError());
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(abortError());
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

// createLimiter returns a function that runs tasks with at most `max` in flight;
// the rest wait in FIFO order.
export function createLimiter(max: number) {
  let active = 0;
  const queue: (() => void)[] = [];
  const next = () => {
    if (active >= max) return;
    const start = queue.shift();
    if (start) {
      active++;
      start();
    }
  };
  return function run<T>(task: () => Promise<T>): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      queue.push(() => {
        Promise.resolve()
          .then(task)
          .then(resolve, reject)
          .finally(() => {
            active--;
            next();
          });
      });
      next();
    });
  };
}
