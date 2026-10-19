import { useEffect, useState } from "react";
import { X } from "lucide-react";
import { setToastHandler, type ToastType } from "../lib/toast";
import { uuid } from "../lib/uuid";
import { useT } from "../lib/i18n/context";

interface Item {
  id: string;
  msg: string;
  type: ToastType;
}

// ToastHost renders transient notifications and registers the global handler so
// the react-query MutationCache can surface failures (otherwise mutations fail
// silently). Auto-dismisses after a few seconds.
//
// The outer live region is always mounted (even when empty) so assistive tech
// has a stable container to observe before any message is inserted. Errors are
// role="alert" (assertive); success/info are role="status" (polite).
export function ToastHost() {
  const t = useT();
  const [items, setItems] = useState<Item[]>([]);

  useEffect(() => {
    setToastHandler((msg, type = "error") => {
      // Not crypto.randomUUID(): it is undefined outside a secure context, so on a
      // plain-HTTP LAN deployment every toast would throw.
      const id = uuid();
      setItems((x) => [...x, { id, msg, type }]);
      setTimeout(() => setItems((x) => x.filter((i) => i.id !== id)), 5000);
    });
    return () => setToastHandler(null);
  }, []);

  const dismiss = (id: string) => setItems((x) => x.filter((i) => i.id !== id));

  return (
    <div
      aria-live="polite"
      aria-atomic="false"
      className="pointer-events-none fixed bottom-4 right-4 z-50 flex flex-col gap-2"
    >
      {items.map((i) => (
        <div
          key={i.id}
          role={i.type === "error" ? "alert" : "status"}
          className={`toast-in pointer-events-auto flex items-center gap-3 rounded-lg px-4 py-2 text-sm text-white shadow-lg ${
            i.type === "error"
              ? "bg-[var(--color-danger-fill)]"
              : i.type === "info"
                ? "bg-[var(--color-accent)]"
                : "bg-[var(--color-success-fill)]"
          }`}
        >
          <span className="flex-1">{i.msg}</span>
          <button
            type="button"
            aria-label={t("common.dismiss")}
            onClick={() => dismiss(i.id)}
            className="-mr-1 shrink-0 rounded p-0.5 text-white/80 transition hover:text-white"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      ))}
    </div>
  );
}
