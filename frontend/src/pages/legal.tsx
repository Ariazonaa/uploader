import { Link } from "react-router-dom";
import { usePublicSettings } from "../lib/api/hooks";
import { useT } from "../lib/i18n/context";

// LegalPage renders the operator-configured legal notice / Impressum. Public.
export function LegalPage() {
  const t = useT();
  const { data: pub, isLoading } = usePublicSettings();
  return (
    <div className="mx-auto max-w-2xl p-6">
      <h1 className="mb-4 text-xl font-semibold">{t("legal.title")}</h1>
      {isLoading ? (
        <p className="text-[var(--color-muted)]">{t("common.loading")}</p>
      ) : pub?.legal_notice ? (
        <pre className="whitespace-pre-wrap font-sans text-sm leading-relaxed">{pub.legal_notice}</pre>
      ) : (
        <p className="text-[var(--color-muted)]">{t("legal.notConfigured")}</p>
      )}
      <p className="mt-6 text-sm">
        <Link to="/" className="text-[var(--color-accent)] underline">
          {t("legal.back")}
        </Link>
      </p>
    </div>
  );
}
