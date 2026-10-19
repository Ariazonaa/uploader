export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

// Date formatting is locale-aware. The I18nProvider keeps `locale` in sync with
// the active UI language via setDateLocale, so formatDate follows the switcher.
// formatBytes stays locale-neutral (fixed units, "." decimal) on purpose.
let locale = "en";

export function setDateLocale(l: string) {
  locale = l;
}

export function formatDate(iso: string): string {
  return new Date(iso).toLocaleString(locale);
}
