import { useEffect, useRef } from "react";
import { Card } from "../components/ui";
import { useT } from "../lib/i18n/context";

// The vendored Swagger UI bundle attaches a global constructor. A minimal shim
// keeps tsc happy without pulling in an npm import (assets are self-hosted under
// /vendor/swagger-ui, so the app stays fully self-contained / CDN-free).
declare global {
  interface Window {
    SwaggerUIBundle?: any;
  }
}

const CSS_ID = "swagger-ui-css";
const JS_ID = "swagger-ui-js";
const CSS_HREF = "/vendor/swagger-ui/swagger-ui.css";
const JS_SRC = "/vendor/swagger-ui/swagger-ui-bundle.js";
const SPEC_URL = "/api/v1/openapi.yaml";
// Swagger UI 5 ships a complete dark theme, scoped under `html.dark-mode`. The
// class only has meaning to swagger-ui.css, so it is set while this page is
// mounted and removed again on the way out (index.css pins the page background
// back to the AMOLED black that theme would otherwise replace).
const DARK_CLASS = "dark-mode";

// DocsPage renders an interactive Swagger UI for the JSON API. Visible only to
// logged-in users (route lives inside ProtectedLayout, and the spec endpoint
// itself 401s when logged out). The heavy Swagger assets are loaded lazily from
// the vendored copies on mount rather than bundled into the SPA.
export function DocsPage() {
  const t = useT();
  const containerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    let cancelled = false;
    const root = document.documentElement;
    root.classList.add(DARK_CLASS);

    // Inject the stylesheet once; keep it across mounts (harmless if present).
    if (!document.getElementById(CSS_ID)) {
      const link = document.createElement("link");
      link.id = CSS_ID;
      link.rel = "stylesheet";
      link.href = CSS_HREF;
      document.head.appendChild(link);
    }

    const init = () => {
      if (cancelled || !window.SwaggerUIBundle || !containerRef.current) return;
      containerRef.current.innerHTML = "";
      window.SwaggerUIBundle({
        url: SPEC_URL,
        dom_id: "#swagger-ui",
        presets: [window.SwaggerUIBundle.presets.apis],
        docExpansion: "list",
        tryItOutEnabled: true,
        // Keep it fully self-contained: never call the external validator badge.
        validatorUrl: null,
      });
    };

    let script = document.getElementById(JS_ID) as HTMLScriptElement | null;
    if (window.SwaggerUIBundle) {
      // Bundle already loaded from a previous visit — render immediately.
      init();
    } else {
      if (!script) {
        script = document.createElement("script");
        script.id = JS_ID;
        script.src = JS_SRC;
        script.async = true;
        document.body.appendChild(script);
      }
      script.addEventListener("load", init);
    }

    return () => {
      cancelled = true;
      root.classList.remove(DARK_CLASS);
      if (script) script.removeEventListener("load", init);
      // Tear down the rendered UI so a remount re-initializes cleanly.
      if (containerRef.current) containerRef.current.innerHTML = "";
    };
  }, []);

  return (
    <div className="mx-auto max-w-6xl p-4 sm:p-6">
      <Card className="p-4 sm:p-6">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h1 className="text-xl font-semibold">{t("docs.title")}</h1>
            <p className="text-sm text-[var(--color-muted)]">{t("docs.subtitle")}</p>
          </div>
          <a
            href={SPEC_URL}
            download
            className="control rounded-full px-4 py-2 text-sm text-[var(--color-text)] transition hover:bg-white/10"
          >
            {t("docs.download")}
          </a>
        </div>
        {/* Swagger UI renders here, in its own dark theme (see DARK_CLASS). */}
        <div ref={containerRef} id="swagger-ui" className="overflow-x-auto rounded-2xl" />
      </Card>
    </div>
  );
}
