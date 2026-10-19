import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MutationCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";
import { App } from "./app";
import { ToastHost } from "./components/toast";
import { I18nProvider } from "./lib/i18n/context";
import { translateApiError } from "./lib/i18n";
import { toast } from "./lib/toast";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 30_000, refetchOnWindowFocus: false } },
  // Surface any failed mutation (delete, save, …) as a toast instead of failing
  // silently. Runs outside React, so it uses the i18n module-level bus
  // (translateApiError) rather than the hook. Components may still show inline
  // errors in addition.
  mutationCache: new MutationCache({
    onError: (err) => toast(translateApiError(err), "error"),
  }),
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <I18nProvider>
        <BrowserRouter>
          <App />
        </BrowserRouter>
        <ToastHost />
      </I18nProvider>
    </QueryClientProvider>
  </StrictMode>,
);
