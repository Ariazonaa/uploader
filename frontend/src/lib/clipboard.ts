// copyToClipboard copies text and reports whether it actually landed on the
// clipboard. navigator.clipboard only exists in a secure context, so on a
// plain-HTTP LAN deployment it falls back to the legacy execCommand path; that
// one needs a recent user gesture and may fail after a long async upload, which
// is why callers get a boolean instead of assuming success.
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* denied or no focus — try the fallback below */
  }
  // Selecting the helper textarea moves focus; give it back afterwards so a
  // copy that finishes in the background doesn't yank the caret out of a field.
  const previous = document.activeElement as HTMLElement | null;
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.setAttribute("readonly", "");
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch {
    return false;
  } finally {
    previous?.focus?.({ preventScroll: true });
  }
}
