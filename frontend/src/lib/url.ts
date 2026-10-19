// safeHttpUrl returns the input only if it is a valid absolute http(s) URL,
// otherwise undefined. Used to sanitize operator-supplied URLs (logo, login
// background) before injecting them into href/src/CSS, blocking javascript:,
// data: and other dangerous schemes.
export function safeHttpUrl(value: string | null | undefined): string | undefined {
  if (!value) return undefined;
  try {
    const u = new URL(value, window.location.origin);
    if (u.protocol === "http:" || u.protocol === "https:") return u.href;
  } catch {
    /* not a valid URL */
  }
  return undefined;
}
