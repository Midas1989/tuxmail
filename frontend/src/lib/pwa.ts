// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Service worker registration and Web Push subscription (browser/PWA only).
// The desktop build (Wails) never registers a service worker; it uses
// native OS notifications from the Go core instead.

import type { Api } from './api/api';
import { isWails, urlBase64ToUint8Array } from './platform';

export function pwaSupported(): boolean {
  return typeof navigator !== 'undefined' && 'serviceWorker' in navigator && !isWails();
}

export function pushSupported(): boolean {
  return pwaSupported() && typeof window !== 'undefined' && 'PushManager' in window && 'Notification' in window;
}

/**
 * Register ./sw.js (emitted by the build, see vite.config.ts). Only in
 * production builds and never inside Wails. Messages from the SW with
 * {type:'navigate', url} are forwarded to `onNavigate` (notification clicks).
 */
export async function registerServiceWorker(onNavigate: (url: string) => void): Promise<ServiceWorkerRegistration | null> {
  if (!pwaSupported() || !import.meta.env.PROD) return null;
  navigator.serviceWorker.addEventListener('message', (e: MessageEvent) => {
    const d = e.data as { type?: string; url?: string } | null;
    if (d?.type === 'navigate' && typeof d.url === 'string') onNavigate(d.url);
  });
  try {
    return await navigator.serviceWorker.register('./sw.js', { scope: './' });
  } catch (err) {
    console.warn('service worker registration failed', err);
    return null;
  }
}

export async function currentPushSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null;
  const reg = await navigator.serviceWorker.getRegistration();
  return (await reg?.pushManager.getSubscription()) ?? null;
}

export type EnablePushResult = 'subscribed' | 'denied' | 'unsupported' | 'no-sw';

export async function enablePush(api: Api): Promise<EnablePushResult> {
  if (!pushSupported()) return 'unsupported';
  const permission = await Notification.requestPermission();
  if (permission !== 'granted') return 'denied';
  const reg = await navigator.serviceWorker.getRegistration();
  if (!reg) return 'no-sw';
  const key = await api.getPushVapidKey();
  const sub = (await reg.pushManager.getSubscription()) ?? (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(key) }));
  const json = sub.toJSON();
  await api.subscribePush({
    endpoint: sub.endpoint,
    keys: { p256dh: json.keys?.p256dh ?? '', auth: json.keys?.auth ?? '' },
    userAgent: navigator.userAgent,
    deviceName: deviceName(),
  });
  return 'subscribed';
}

export async function disablePush(api: Api): Promise<void> {
  const sub = await currentPushSubscription();
  if (!sub) return;
  await api.unsubscribePush(sub.endpoint);
  await sub.unsubscribe();
}

function deviceName(): string {
  const ua = navigator.userAgent;
  if (/iphone/i.test(ua)) return 'iPhone';
  if (/ipad/i.test(ua)) return 'iPad';
  if (/android/i.test(ua)) return 'Android';
  if (/windows/i.test(ua)) return 'Windows';
  if (/mac os/i.test(ua)) return 'Mac';
  if (/linux/i.test(ua)) return 'Linux';
  return 'Browser';
}
