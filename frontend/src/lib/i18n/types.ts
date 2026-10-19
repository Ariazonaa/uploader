import type { en } from "./en";

// Lang is the set of supported UI languages.
export type Lang = "en" | "de";

// MessageKey is every catalog key. It is derived from the English catalog, which
// is the single source of truth: adding/removing a key in en.ts automatically
// flows into this union, and de.ts (typed Record<MessageKey, string>) must then
// carry exactly the same keys or tsc fails — that is the completeness gate.
export type MessageKey = keyof typeof en;

// Vars are the interpolation values for a message ({name} → vars.name).
export type Vars = Record<string, string | number>;

// PluralBase is any key prefix that has both a `<base>_one` and (implicitly)
// `<base>_other` form. Typing plural()/tn() against it means a typo'd base or a
// base without plural forms is a compile-time error. The indirection through a
// generic parameter is required so the conditional distributes over the
// MessageKey union (a bare `MessageKey extends …` would evaluate as a whole).
type PluralBaseOf<K> = K extends `${infer B}_one` ? B : never;
export type PluralBase = PluralBaseOf<MessageKey>;
