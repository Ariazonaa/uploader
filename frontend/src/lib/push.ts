import { ApiError, api } from "./api/client";
import { tGlobal } from "./i18n";

// urlBase64ToUint8Array converts a base64url VAPID key to the format the
// PushManager expects.
function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(base64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

export function pushSupported(): boolean {
  return "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

async function localSubscription(): Promise<PushSubscription | null> {
  const reg = await navigator.serviceWorker.getRegistration();
  return (await reg?.pushManager.getSubscription()) ?? null;
}

// subscribePush registers the SW, asks for permission, subscribes and persists
// the subscription server-side. The browser subscription is only kept when the
// server stored it too: a local subscription the server doesn't know about
// would show "enabled" while no notification can ever arrive.
export async function subscribePush(vapidPublicKey: string): Promise<void> {
  if (!pushSupported()) throw new Error(tGlobal("account.pushUnsupported"));
  await navigator.serviceWorker.register("/sw.js");
  const perm = await Notification.requestPermission();
  if (perm !== "granted") throw new Error(tGlobal("account.pushPermissionDenied"));
  // subscribe() needs an active worker; register() resolves before the new
  // worker has activated, so wait for it.
  const reg = await navigator.serviceWorker.ready;
  const options = {
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(vapidPublicKey) as BufferSource,
  };
  let sub = await reg.pushManager.subscribe(options);
  try {
    await api.post("/api/v1/push/subscribe", sub.toJSON());
  } catch (e) {
    await sub.unsubscribe().catch(() => {});
    // 409: this browser's endpoint is registered to another account (someone
    // else enabled notifications here). A fresh subscription gets a new
    // endpoint, which this account can claim.
    if (!(e instanceof ApiError && e.status === 409)) throw e;
    sub = await reg.pushManager.subscribe(options);
    try {
      await api.post("/api/v1/push/subscribe", sub.toJSON());
    } catch (e2) {
      await sub.unsubscribe().catch(() => {});
      throw e2;
    }
  }
}

// unsubscribePush removes the local + server-side subscription.
export async function unsubscribePush(): Promise<void> {
  const sub = await localSubscription();
  if (sub) {
    await api.post("/api/v1/push/unsubscribe", { endpoint: sub.endpoint });
    await sub.unsubscribe();
  }
}

// isPushSubscribed reports whether this browser is subscribed *for this
// account*. There is no endpoint to list subscriptions, so a local one is
// re-registered with the server (an idempotent upsert): that also repairs a
// subscription the server pruned or never stored. When the server rejects it
// (another account owns the endpoint, push got disabled) it counts as off.
export async function isPushSubscribed(): Promise<boolean> {
  if (!pushSupported()) return false;
  const sub = await localSubscription();
  if (!sub) return false;
  try {
    await api.post("/api/v1/push/subscribe", sub.toJSON());
    return true;
  } catch {
    return false;
  }
}
