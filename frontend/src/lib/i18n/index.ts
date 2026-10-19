// Lightweight, dependency-free i18n engine. The React-facing API lives in
// context.tsx; this module holds the pure translate/plural functions plus a
// module-level "current language" bus (mirroring lib/toast.ts) so code outside
// the React tree — the react-query MutationCache, lib/filetype.ts helpers — can
// still translate via tGlobal()/translateApiError().

import { en } from "./en";
import { de } from "./de";
import type { Lang, MessageKey, PluralBase, Vars } from "./types";

const catalogs: Record<Lang, Record<MessageKey, string>> = { en, de };

// isDev is a small guard so we can DEV-warn on missing keys without depending on
// vite/client ambient types (kept dependency-free by design).
function isDev(): boolean {
  try {
    return Boolean((import.meta as { env?: { DEV?: boolean } }).env?.DEV);
  } catch {
    return false;
  }
}

// interpolate replaces {var} occurrences that have a matching value in `vars`.
// Placeholders without a matching var (e.g. the literal embed tokens {filename},
// {size}) are left verbatim, which is exactly what the settings hints want.
function interpolate(str: string, vars?: Vars): string {
  if (!vars) return str;
  return str.replace(/\{(\w+)\}/g, (m, key: string) => (key in vars ? String(vars[key]) : m));
}

// translate resolves a key in the active language, falling back to English and
// then to the raw key (with a DEV warning) so the UI never renders "undefined".
export function translate(lang: Lang, key: MessageKey, vars?: Vars): string {
  const active = catalogs[lang] ?? en;
  let msg: string | undefined = active[key];
  if (msg === undefined) msg = en[key];
  if (msg === undefined) {
    if (isDev()) console.warn(`[i18n] missing key: ${key}`);
    return key;
  }
  return interpolate(msg, vars);
}

// plural selects the `<base>_one` / `<base>_other` form via Intl.PluralRules and
// always exposes `count` as an interpolation var. Unknown CLDR categories fall
// back to `_other`.
export function plural(lang: Lang, count: number, base: PluralBase, vars?: Vars): string {
  const category = new Intl.PluralRules(lang).select(count);
  const candidate = `${base}_${category}` as MessageKey;
  const key: MessageKey = candidate in en ? candidate : (`${base}_other` as MessageKey);
  return translate(lang, key, { count, ...vars });
}

// --- Module-level language bus (for non-React callers) ------------------------

let currentLang: Lang = "en";

export function setCurrentLang(lang: Lang) {
  currentLang = lang;
}

export function getLang(): Lang {
  return currentLang;
}

// tGlobal translates in the current language for callers that can't use the hook.
export function tGlobal(key: MessageKey, vars?: Vars): string {
  return translate(currentLang, key, vars);
}

// --- Language detection / persistence -----------------------------------------

const STORAGE_KEY = "lang";

export function detectInitialLang(): Lang {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored === "en" || stored === "de") return stored;
  } catch {
    /* localStorage may be unavailable (private mode) — fall through */
  }
  if (typeof navigator !== "undefined" && navigator.language?.toLowerCase().startsWith("de")) {
    return "de";
  }
  return "en";
}

export function persistLang(lang: Lang) {
  try {
    localStorage.setItem(STORAGE_KEY, lang);
  } catch {
    /* ignore persistence failures */
  }
}

// --- Backend error mapping ----------------------------------------------------

// Known backend error messages (see the writeErr calls in backend/internal/api)
// mapped to friendly, localized ones. Matched whole (case-insensitive), never by
// substring: "invite" also occurs in "username, email and invite code are
// required" and "not allowed" in "method not allowed", which a substring match
// turned into the wrong explanation.
const API_ERRORS: Record<string, MessageKey> = {
  "invalid credentials": "errors.invalidCredentials",
  "password is incorrect": "errors.wrongPassword",
  "that slug is already taken": "errors.slugTaken",
  "invalid or already used invite code": "errors.inviteInvalid",
  "invite not found or already used": "errors.inviteInvalid",
  "file too large": "errors.fileTooLarge",
  "this file type is not allowed": "errors.blockedType",
  "invalid 2fa code": "errors.invalidTotp",
  "incorrect code": "errors.invalidTotp",
  unauthorized: "errors.unauthorized",
};

// Messages the server builds with variable detail, matched by their fixed start.
const API_ERROR_PREFIXES: [string, MessageKey][] = [
  ["too many unfinished uploads", "errors.tooManyUploads"],
  ["token limit reached", "errors.tokenLimit"],
  ["this is the only admin account", "errors.lastAdmin"],
];

// translateApiError maps known backend error strings to friendly, localized
// messages. Anything unknown is returned as-is so operators still see the
// original server text.
export function translateApiError(err: unknown): string {
  const raw = err instanceof Error ? err.message : typeof err === "string" ? err : "";
  if (!raw) return tGlobal("errors.generic");
  const m = raw.trim().toLowerCase();

  const exact = API_ERRORS[m];
  if (exact) return tGlobal(exact);
  for (const [prefix, key] of API_ERROR_PREFIXES) {
    if (m.startsWith(prefix)) return tGlobal(key);
  }
  return raw;
}
