import {
  keepPreviousData,
  type QueryClient,
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { ApiError, api } from "./client";
import { ACTIVITY_DAYS, localTimeZone } from "../activity";
import type {
  ApiToken,
  AuditPage,
  DayCount,
  FileItem,
  InviteCode,
  PublicSettings,
  SessionItem,
  Settings,
  Stats,
  TotpChallenge,
  UploadDefaults,
  User,
} from "./types";

const PAGE_SIZE = 60;

export function useMe() {
  return useQuery<User | null>({
    queryKey: ["me"],
    queryFn: async () => {
      try {
        return await api.get<User>("/api/v1/auth/me");
      } catch (e) {
        // Only an auth failure means "logged out". Network/5xx errors must
        // surface as a query error instead of falsely redirecting to /login.
        if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
          return null;
        }
        throw e;
      }
    },
    retry: false,
  });
}

// switchSession drops everything cached for the previous account before the
// new identity is set. Logging in must do it too, not just logging out: a
// session that expired (or a logout that failed) leaves the cache behind, and
// the next user would see the previous one's files, tokens and settings.
// `me` is overwritten in place rather than removed so its mounted observers
// re-render, and the public settings aren't per-account - dropping them would
// blank the whole app (SetupGate) while they refetch.
function switchSession(qc: QueryClient, me: User | null) {
  qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "me" && q.queryKey[0] !== "public-settings" });
  qc.setQueryData(["me"], me);
}

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (creds: { username: string; password: string }) =>
      api.post<User | TotpChallenge>("/api/v1/auth/login", creds),
    onSuccess: (res) => {
      // Only a full user (not a 2FA challenge) means we're logged in.
      if (res && "id" in res) switchSession(qc, res);
    },
  });
}

export function useLoginTotp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { pending: string; code: string }) => api.post<User>("/api/v1/auth/login/totp", body),
    onSuccess: (user) => switchSession(qc, user),
  });
}

// Setup and enable both re-check the account password (the server refuses
// either without it).
export function useTotpSetup() {
  return useMutation({
    mutationFn: (password: string) =>
      api.post<{ secret: string; otpauth_url: string }>("/api/v1/auth/totp/setup", { password }),
  });
}

export function useTotpEnable() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { code: string; password: string }) =>
      api.post<{ status: string; recovery_codes: string[] }>("/api/v1/auth/totp/enable", body),
    // Not awaited: the caller's onSuccess, which keeps the one-time recovery
    // codes, must run before the refetched `me` re-renders the 2FA card.
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["me"] });
    },
  });
}

export function useTotpDisable() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (password: string) => api.post("/api/v1/auth/totp/disable", { password }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["me"] }),
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post("/api/v1/auth/logout"),
    // Settled, not success: an expired session answers 401, and the user is
    // logged out either way.
    onSettled: () => switchSession(qc, null),
  });
}

export interface FileFilter {
  q?: string;
  type?: string; // mime prefix, e.g. "image/"
  sort?: "created" | "size" | "views" | "name";
  order?: "asc" | "desc";
}

export function useFiles(filter: FileFilter = {}) {
  return useInfiniteQuery({
    queryKey: ["files", filter],
    queryFn: ({ pageParam }) => {
      const p = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(pageParam) });
      if (filter.q) p.set("q", filter.q);
      if (filter.type) p.set("type", filter.type);
      if (filter.sort) p.set("sort", filter.sort);
      if (filter.order) p.set("order", filter.order);
      return api.get<FileItem[]>(`/api/v1/files?${p.toString()}`);
    },
    initialPageParam: 0,
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === PAGE_SIZE ? allPages.length * PAGE_SIZE : undefined,
  });
}

export function useStats() {
  return useQuery<Stats>({ queryKey: ["stats"], queryFn: () => api.get<Stats>("/api/v1/stats") });
}

export function useActivity() {
  // Days are bucketed server-side in the browser's zone so they line up with the
  // local-date keys the heatmap and streaks use (see lib/activity.ts dayKey).
  const tz = localTimeZone();
  return useQuery<DayCount[]>({
    queryKey: ["activity", tz],
    queryFn: () => {
      const p = new URLSearchParams({ days: String(ACTIVITY_DAYS) });
      if (tz) p.set("tz", tz);
      return api.get<DayCount[]>(`/api/v1/stats/activity?${p.toString()}`);
    },
  });
}

// removeFromFilePages drops the given ids from a cached infinite-query of files.
type FilePages = { pages: FileItem[][]; pageParams: unknown[] } | undefined;
function dropFiles(old: FilePages, ids: Set<string>): FilePages {
  if (!old?.pages) return old;
  return { ...old, pages: old.pages.map((p) => p.filter((f) => !ids.has(f.id))) };
}

export function useDeleteFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/v1/files/${id}`),
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: ["files"] });
      const prev = qc.getQueriesData<FilePages>({ queryKey: ["files"] });
      qc.setQueriesData<FilePages>({ queryKey: ["files"] }, (old) => dropFiles(old, new Set([id])));
      return { prev };
    },
    onError: (_e, _id, ctx) => {
      if (ctx?.prev) ctx.prev.forEach(([key, data]) => qc.setQueryData(key, data));
      else qc.invalidateQueries({ queryKey: ["files"] });
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["files"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
      qc.invalidateQueries({ queryKey: ["trash"] });
    },
  });
}

export function useRenameFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; name: string }) => api.patch(`/api/v1/files/${v.id}`, { name: v.name }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["files"] }),
  });
}

export function useBulkDeleteFiles() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (ids: string[]) => api.post<{ deleted: number }>("/api/v1/files/bulk-delete", { ids }),
    onMutate: async (ids) => {
      await qc.cancelQueries({ queryKey: ["files"] });
      const prev = qc.getQueriesData<FilePages>({ queryKey: ["files"] });
      qc.setQueriesData<FilePages>({ queryKey: ["files"] }, (old) => dropFiles(old, new Set(ids)));
      return { prev };
    },
    onError: (_e, _ids, ctx) => {
      if (ctx?.prev) ctx.prev.forEach(([key, data]) => qc.setQueryData(key, data));
      else qc.invalidateQueries({ queryKey: ["files"] });
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ["files"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
      qc.invalidateQueries({ queryKey: ["trash"] });
    },
  });
}

export function useTokens() {
  return useQuery<ApiToken[]>({
    queryKey: ["tokens"],
    queryFn: () => api.get<ApiToken[]>("/api/v1/tokens"),
  });
}

export function useCreateToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.post<{ name: string; token: string }>("/api/v1/tokens", { name }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });
}

export function useDeleteToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/v1/tokens/${id}`),
    // Optimistically drop the row so it disappears immediately; roll back if the
    // server rejects, and always re-sync from the server afterwards.
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: ["tokens"] });
      const prev = qc.getQueryData<ApiToken[]>(["tokens"]);
      qc.setQueryData<ApiToken[]>(["tokens"], (old) => (old ?? []).filter((t) => t.id !== id));
      return { prev };
    },
    onError: (_e, _id, ctx) => {
      if (ctx?.prev) qc.setQueryData(["tokens"], ctx.prev);
      else qc.invalidateQueries({ queryKey: ["tokens"] });
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ["tokens"] }),
  });
}

// useTokenSelfTest uploads a tiny 1x1 PNG with a freshly created token to
// verify it works end to end. It authenticates with the token instead of the
// session, so it can't use api.post, but fails the same way (ApiError with the
// server's message) and runs as a mutation, so a failure gets the shared toast.
export function useTokenSelfTest() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (token: string) => {
      // 1x1 transparent PNG.
      const b64 =
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC";
      const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
      const form = new FormData();
      form.append("file", new Blob([bytes], { type: "image/png" }), "selftest.png");
      const res = await fetch("/api/v1/upload", {
        method: "POST",
        credentials: "omit",
        cache: "no-store",
        headers: { Authorization: `Bearer ${token}` },
        body: form,
      });
      const data = (await res.json().catch(() => null)) as { url?: string; error?: string } | null;
      if (!res.ok || !data?.url) {
        throw new ApiError(res.status, data?.error ? String(data.error) : `request failed (${res.status})`);
      }
      return { url: data.url };
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["files"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
    },
  });
}

export function useSettings() {
  return useQuery<Settings>({
    queryKey: ["settings"],
    queryFn: () => api.get<Settings>("/api/v1/settings"),
  });
}

export function useUpdateSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (patch: Partial<Settings>) => api.patch<Settings>("/api/v1/settings", patch),
    onSuccess: (s) => {
      qc.setQueryData(["settings"], s);
      // Branding, legal notice, registration and the encryption default are
      // derived from the settings; refetch them so the change shows at once.
      qc.invalidateQueries({ queryKey: ["public-settings"] });
      qc.invalidateQueries({ queryKey: ["upload-defaults"] });
    },
  });
}

// useUploadDefaults reads the server's encryption default for the uploader's
// toggle.
export function useUploadDefaults() {
  return useQuery<UploadDefaults>({
    queryKey: ["upload-defaults"],
    queryFn: () => api.get<UploadDefaults>("/api/v1/upload/defaults"),
    staleTime: 60_000,
  });
}

// usePushTest asks the server to send a test notification to this account.
export function usePushTest() {
  return useMutation({ mutationFn: () => api.post("/api/v1/push/test") });
}

export function useSessions() {
  return useQuery<SessionItem[]>({
    queryKey: ["sessions"],
    queryFn: () => api.get<SessionItem[]>("/api/v1/auth/sessions"),
  });
}

export function useRevokeSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/v1/auth/sessions/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
  });
}

export function useRevokeOtherSessions() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post("/api/v1/auth/sessions/revoke-others"),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
  });
}

export function useChangePassword() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { current_password: string; new_password: string }) =>
      api.post("/api/v1/auth/password", body),
    // The server revokes every other session on a password change, so the
    // cached session list is stale the moment this succeeds.
    onSuccess: () => qc.invalidateQueries({ queryKey: ["sessions"] }),
  });
}

export function useDeleteAccount() {
  return useMutation({
    mutationFn: (password: string) => api.delete("/api/v1/auth/account", { password }),
  });
}

export function usePublicSettings() {
  return useQuery<PublicSettings>({
    queryKey: ["public-settings"],
    queryFn: () => api.get<PublicSettings>("/api/v1/public/settings"),
  });
}

export function useSetup() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: { username: string; email: string; password: string }) =>
      api.post<User>("/api/v1/auth/setup", body),
    onSuccess: (user) => {
      switchSession(qc, user);
      qc.invalidateQueries({ queryKey: ["public-settings"] });
    },
  });
}

// --- Trash (soft-delete) ---

export function useTrash() {
  return useInfiniteQuery({
    queryKey: ["trash"],
    queryFn: ({ pageParam }) => {
      const p = new URLSearchParams({ limit: String(PAGE_SIZE), offset: String(pageParam) });
      return api.get<FileItem[]>(`/api/v1/files/trash?${p.toString()}`);
    },
    initialPageParam: 0,
    getNextPageParam: (lastPage, allPages) =>
      lastPage.length === PAGE_SIZE ? allPages.length * PAGE_SIZE : undefined,
  });
}

export function useRestoreFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.post(`/api/v1/files/${id}/restore`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["trash"] });
      qc.invalidateQueries({ queryKey: ["files"] });
      qc.invalidateQueries({ queryKey: ["stats"] });
    },
  });
}

export function usePurgeFile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.delete(`/api/v1/files/${id}/purge`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["trash"] }),
  });
}

export function useEmptyTrash() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post("/api/v1/files/trash/empty"),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["trash"] }),
  });
}

export function useRegister() {
  return useMutation({
    mutationFn: (body: { username: string; email: string; password: string; invite_code: string }) =>
      api.post("/api/v1/auth/register", body),
  });
}

export function useInvites() {
  return useQuery<InviteCode[]>({
    queryKey: ["invites"],
    queryFn: () => api.get<InviteCode[]>("/api/v1/invites"),
  });
}

export function useCreateInvite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (expiresInDays: number) =>
      api.post<{ code: string }>("/api/v1/invites", { expires_in_days: expiresInDays }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["invites"] }),
  });
}

export function useRevokeInvite() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (code: string) => api.delete(`/api/v1/invites/${encodeURIComponent(code)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["invites"] }),
  });
}

export interface ViewStats {
  referrers: { name: string; count: number }[];
  agents: { name: string; count: number }[];
}

export function useViewStats() {
  return useQuery<ViewStats>({
    queryKey: ["view-stats"],
    queryFn: () => api.get<ViewStats>("/api/v1/stats/views"),
  });
}

const AUDIT_PAGE_SIZE = 50;

export function useAuditLog({ event, user, page = 0 }: { event?: string; user?: string; page?: number }) {
  return useQuery<AuditPage>({
    queryKey: ["audit", event ?? "", user ?? "", page],
    queryFn: () => {
      const p = new URLSearchParams({
        limit: String(AUDIT_PAGE_SIZE),
        offset: String(page * AUDIT_PAGE_SIZE),
      });
      if (event) p.set("event", event);
      if (user) p.set("user", user);
      return api.get<AuditPage>(`/api/v1/audit?${p.toString()}`);
    },
    // Keep the previous page visible while the next one loads (no empty table
    // flashing between pages / filter changes).
    placeholderData: keepPreviousData,
  });
}
