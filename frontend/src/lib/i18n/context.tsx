import { createContext, useContext, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import type { Lang, MessageKey, PluralBase, Vars } from "./types";
import { detectInitialLang, persistLang, plural, setCurrentLang, translate } from "./index";
import { setDateLocale } from "../format";

type TFn = (key: MessageKey, vars?: Vars) => string;
type TnFn = (base: PluralBase, count: number, vars?: Vars) => string;

interface I18nContextValue {
  lang: Lang;
  setLang: (lang: Lang) => void;
  t: TFn;
  tn: TnFn;
}

const I18nContext = createContext<I18nContextValue | null>(null);

// I18nProvider owns the active language. It also keeps the non-React surfaces in
// sync: the module-level bus (setCurrentLang, used by tGlobal/translateApiError),
// <html lang>, and the date-formatting locale in lib/format.
export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(detectInitialLang);

  useEffect(() => {
    setCurrentLang(lang);
    document.documentElement.lang = lang;
    setDateLocale(lang);
  }, [lang]);

  const value = useMemo<I18nContextValue>(() => {
    const setLang = (next: Lang) => {
      persistLang(next);
      setLangState(next);
    };
    return {
      lang,
      setLang,
      t: (key, vars) => translate(lang, key, vars),
      tn: (base, count, vars) => plural(lang, count, base, vars),
    };
  }, [lang]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext);
  if (!ctx) throw new Error("useI18n must be used within an I18nProvider");
  return ctx;
}

export function useT(): TFn {
  return useI18n().t;
}

export function useTn(): TnFn {
  return useI18n().tn;
}

// LanguageSwitcher is a Liquid Glass segmented DE | EN control, styled like the
// dashboard grid/list toggle (control pill, active = bg-white/10).
export function LanguageSwitcher({ className = "" }: { className?: string }) {
  const { lang, setLang, t } = useI18n();
  const options: { code: Lang; label: string }[] = [
    { code: "de", label: "DE" },
    { code: "en", label: "EN" },
  ];
  return (
    <div
      role="group"
      aria-label={t("common.language")}
      className={`control flex gap-0.5 rounded-full p-0.5 ${className}`}
    >
      {options.map((o) => (
        <button
          key={o.code}
          type="button"
          onClick={() => setLang(o.code)}
          aria-pressed={lang === o.code}
          className={`rounded-full px-2.5 py-1 text-xs font-medium transition ${
            lang === o.code
              ? "bg-white/10 text-[var(--color-text)]"
              : "text-[var(--color-muted)] hover:text-[var(--color-text)]"
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
