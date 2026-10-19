// Tiny module-level toast bus so code outside the React tree (e.g. the
// react-query MutationCache) can raise toasts. The ToastHost component
// registers the handler on mount.

export type ToastType = "error" | "success" | "info";
type ToastFn = (msg: string, type?: ToastType) => void;

let handler: ToastFn | null = null;

export function setToastHandler(fn: ToastFn | null) {
  handler = fn;
}

export function toast(msg: string, type: ToastType = "error") {
  handler?.(msg, type);
}
