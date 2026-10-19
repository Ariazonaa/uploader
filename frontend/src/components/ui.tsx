import type { ButtonHTMLAttributes, InputHTMLAttributes, SelectHTMLAttributes } from "react";
import { CircleAlert } from "lucide-react";
import { useT } from "../lib/i18n/context";

export function Button({
  className = "",
  variant = "primary",
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: "primary" | "ghost" | "danger" }) {
  const base =
    "inline-flex items-center justify-center gap-2 rounded-full px-4 py-2 text-sm font-medium transition active:scale-[0.97] disabled:opacity-50 disabled:cursor-not-allowed";
  const variants = {
    primary: "fill-accent text-white hover:brightness-110",
    ghost: "text-[var(--color-muted)] hover:text-[var(--color-text)] hover:bg-white/10",
    danger: "fill-danger text-white hover:brightness-110",
  };
  // Default to type="button" so a button outside an explicit submit role never
  // accidentally submits a surrounding form (callers set type="submit" where
  // they actually want submission). An explicit type in props still wins.
  return <button type="button" className={`${base} ${variants[variant]} ${className}`} {...props} />;
}

export function Input({ className = "", ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={`control w-full rounded-full px-4 py-2 text-sm text-[var(--color-text)] outline-none transition placeholder:text-[var(--color-muted)] focus:border-[color:var(--color-accent)] focus:shadow-[0_0_0_3px_var(--color-accent-soft)] ${className}`}
      {...props}
    />
  );
}

export function Select({ className = "", ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className={`control w-full cursor-pointer rounded-full px-4 py-2 text-sm text-[var(--color-text)] outline-none transition focus:border-[color:var(--color-accent)] focus:shadow-[0_0_0_3px_var(--color-accent-soft)] ${className}`}
      {...props}
    />
  );
}

export function Card({ children, className = "", ...rest }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={`panel rounded-[var(--radius-card)] ${className}`} {...rest}>
      {children}
    </div>
  );
}

// LoadError is the shared "this failed to load" state with a retry button, so
// a failed query never masquerades as an empty list.
export function LoadError({ onRetry, className = "" }: { onRetry: () => void; className?: string }) {
  const t = useT();
  return (
    <div role="alert" className={`panel rounded-[var(--radius-card)] p-12 text-center ${className}`}>
      <CircleAlert className="mx-auto mb-3 h-12 w-12 text-[var(--color-danger)]" aria-hidden="true" />
      <p className="mb-4 font-medium">{t("common.loadError")}</p>
      <Button variant="ghost" onClick={onRetry}>
        {t("common.retry")}
      </Button>
    </div>
  );
}
