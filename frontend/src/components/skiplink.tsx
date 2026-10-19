// SkipLink lets keyboard users jump straight to the main content, bypassing the
// nav. It is visually hidden until focused (Tailwind sr-only / not-sr-only).
export function SkipLink() {
  return (
    <a
      href="#main"
      className="sr-only focus:not-sr-only focus:fixed focus:left-3 focus:top-3 focus:z-50 focus:rounded-full focus:bg-[var(--color-accent)] focus:px-4 focus:py-2 focus:text-white"
    >
      Skip to content
    </a>
  );
}
