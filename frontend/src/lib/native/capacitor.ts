// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Capacitor bridge of the Android app (mobile/, KONZEPT 15.9 stage A).
// Loaded only inside the native shell (dynamic import in main.ts), so none of
// this reaches the browser/PWA or the Wails desktop app.
//
// bootNative() runs before <App> is mounted:
//   - app lock (if enabled),
//   - active server profile + device token from secure storage,
//   - no usable profile -> "Server verbinden" screen instead of the app.
// startNative() runs after <App> is mounted: back button, deep links,
// share target, push registration, system bars, splash screen, 401 handling.

import { mount, unmount } from 'svelte';
import { Capacitor } from '@capacitor/core';
import { app, prefs, team } from '../state';
import { t } from '../i18n/index.svelte';
import { App, Browser, Preferences, SplashScreen, SystemBars, SystemBarsStyle, TuxMail, type PushRegistration, type SharePayload } from './plugins';
import { ProfileStore, type KeyValueStore, type ServerProfile } from './profiles';
import { readActiveMirror, setSessionToken, setUnauthorizedHandler, writeActiveMirror } from './session';
import { decideBack, dispatchBackEvent, parseConnectLink, parseDeepLink, type BackAction, type DeepLinkTarget } from './navigation';
import { dispatchShare, dropFiles, shareRecipients, shareSubject, shareTextToHtml, sharedFilesToFiles, waitForElement } from './share';
import { fetchVapidKey, registrationKey, subscribeAtServer, type PushSession } from './push';
import { loadLockSettings, saveLockSettings, shouldLock, type LockSettings } from './applock';
import type { NativeController, PushStatus } from './status';
import { startNativeEffects } from './effects.svelte';
import { createDeferredSet, createLockGate } from './lockgate';
import { cancelConnect, confirmConnect, requestConnect, type ConnectPromptState } from './connectprompt';
import ServerConnect from './ServerConnect.svelte';
import ConnectLinkDialog from './ConnectLinkDialog.svelte';
import AppLock from './AppLock.svelte';
import NativeSettings from './NativeSettings.svelte';
import nativeCss from './native.css?inline';
import { deviceName } from './device';
import { installOpenFileHandler } from './files';

const prefsStore: KeyValueStore = {
  get: async (key) => (await Preferences.get({ key })).value,
  set: (key, value) => Preferences.set({ key, value }),
  remove: (key) => Preferences.remove({ key }),
};

const secureStore: KeyValueStore = {
  get: async (key) => (await TuxMail.secureGet({ key })).value,
  set: (key, value) => TuxMail.secureSet({ key, value }),
  remove: (key) => TuxMail.secureRemove({ key }),
};

const profiles = new ProfileStore(prefsStore, secureStore);
const PUSH_KEY = 'tuxmail.push.registered';

let active: ServerProfile | null = null;
let activeToken = '';
let backgroundSince: number | null = null;
let locked = false;

// NG-03: nothing arriving from outside (deep links, notification taps, share intents) may act on the app
// while the lock overlay is shown; it is queued and applied after the unlock.
type Inbound = { kind: 'link'; url: string } | { kind: 'share'; payload: SharePayload };
let linkHandler: (url: string) => void = () => {};
let shareHandler: (p: SharePayload) => void = () => {};
const inbound = createLockGate<Inbound>((item) => (item.kind === 'link' ? linkHandler(item.url) : shareHandler(item.payload)));
// Opening a conversation dismisses its notifications; behind the lock this waits for the unlock.
const pendingCancels = createDeferredSet<number>(
  (id) => void TuxMail.cancelConversationNotifications({ conversationId: String(id) }).catch(() => {}),
  () => locked,
);
let pushStatus: PushStatus = { state: 'idle' };

function injectStyles(): void {
  document.documentElement.classList.add('tm-native');
  const style = document.createElement('style');
  style.dataset.tuxmail = 'native';
  style.textContent = nativeCss;
  document.head.appendChild(style);
}

function hideSplash(): void {
  void SplashScreen.hide({ fadeOutDuration: 150 }).catch(() => {});
}

// ---------------------------------------------------------------------------
// App lock
// ---------------------------------------------------------------------------

async function authenticate(): Promise<boolean> {
  try {
    await TuxMail.authenticate({ title: t('native.lock.prompt'), subtitle: t('native.lock.promptSubtitle'), cancel: t('common.action.cancel') });
    return true;
  } catch {
    return false;
  }
}

function showLock(): Promise<void> {
  if (locked) return Promise.resolve();
  locked = true;
  inbound.lock();
  hideSplash();
  return new Promise((resolve) => {
    const host = document.createElement('div');
    document.body.appendChild(host);
    const cmp = mount(AppLock, {
      target: host,
      props: {
        unlock: authenticate,
        onunlocked: () => {
          void unmount(cmp);
          host.remove();
          locked = false;
          inbound.unlock();
          pendingCancels.flush();
          resolve();
        },
      },
    });
  });
}

async function watchLock(settings: () => Promise<LockSettings>): Promise<void> {
  await App.addListener('appStateChange', ({ isActive }) => {
    if (!isActive) {
      backgroundSince = Date.now();
      return;
    }
    void settings().then((s) => {
      if (shouldLock(s, backgroundSince, Date.now())) void showLock();
      backgroundSince = null;
    });
  });
}

// ---------------------------------------------------------------------------
// Boot
// ---------------------------------------------------------------------------

/**
 * Prepare the native session. Calls `mountApp` when a profile with a token
 * exists, otherwise shows the connect screen in `target`.
 */
export async function bootNative(target: HTMLElement, mountApp: () => void): Promise<void> {
  injectStyles();
  const lock = await loadLockSettings(prefsStore).catch(() => null);
  void watchLock(() => loadLockSettings(prefsStore));
  if (lock && shouldLock(lock, null, Date.now())) await showLock();

  const list = await profiles.list().catch(() => [] as ServerProfile[]);
  active = await profiles.active().catch(() => null);
  const token = active && !active.needsLogin ? await profiles.token(active.id).catch(() => null) : null;
  const mirror = readActiveMirror();

  if (active && token) {
    if (!mirror || mirror.profileId !== active.id || mirror.serverUrl !== active.serverUrl) {
      // The transport was created from a stale mirror: fix it and start over.
      writeActiveMirror({ profileId: active.id, serverUrl: active.serverUrl });
      const fixed = readActiveMirror();
      // Reload only if the mirror is now correct (no reload loop without localStorage).
      if (fixed?.profileId === active.id && fixed.serverUrl === active.serverUrl) {
        location.reload();
        return;
      }
    }
    activeToken = token;
    setSessionToken(token);
    setUnauthorizedHandler(onUnauthorized);
    await TuxMail.setActiveSession({ serverUrl: active.serverUrl, token, profileId: active.id }).catch(() => {});
    mountApp();
    return;
  }

  if (mirror) writeActiveMirror(null);
  const launch = await App.getLaunchUrl().catch(() => undefined);
  const render = showConnect(target, list, active?.serverUrl ?? '', !!active?.needsLogin);
  // A connect link never pre-fills silently: the user confirms the full host first (NG-01).
  if (launch?.url) promptConnect(launch.url, (server) => render(server, true));
}

// ---------------------------------------------------------------------------
// tuxmail://connect confirmation (NG-01)
// ---------------------------------------------------------------------------

let closePrompt: (() => void) | null = null;

/**
 * Ask the user whether to add the server of a tuxmail://connect link. The
 * current session (UI, profile, token) is left untouched; `proceed` runs only
 * after an explicit confirmation. Links that are not https (or loopback) are refused.
 */
export function promptConnect(url: string, proceed: (serverUrl: string) => void): void {
  let state: ConnectPromptState = requestConnect(url, active?.serverUrl ?? null);
  if (state.phase !== 'confirm') return;
  closePrompt?.();
  const { host, current } = state;
  const el = document.createElement('div');
  document.body.appendChild(el);
  const cmp = mount(ConnectLinkDialog, {
    target: el,
    props: {
      host,
      current,
      onconfirm: () => {
        state = confirmConnect(state);
        close();
        if (state.phase === 'connect') proceed(state.server);
      },
      oncancel: () => {
        state = cancelConnect();
        close();
      },
    },
  });
  function close() {
    void unmount(cmp);
    el.remove();
    if (closePrompt === close) closePrompt = null;
  }
  closePrompt = close;
}

/** Login screen for the confirmed server as an overlay; the running session stays active until the new login succeeded. */
async function showConnectOverlay(server: string): Promise<void> {
  const list = await profiles.list().catch(() => [] as ServerProfile[]);
  const host = document.createElement('div');
  host.setAttribute('role', 'dialog');
  host.setAttribute('aria-modal', 'true');
  document.body.appendChild(host);
  let cmp: ReturnType<typeof mount> | null = null;
  const close = () => {
    if (cmp) void unmount(cmp);
    cmp = null;
    host.remove();
  };
  host.addEventListener('keydown', (e) => e.key === 'Escape' && close());
  cmp = mount(ServerConnect, { target: host, props: { ...connectProps(list, server, false, true), oncancel: close } });
}

function connectProps(list: ServerProfile[], prefill: string, expired: boolean, fromLink: boolean) {
  return {
    store: profiles,
    profiles: list,
    deviceName: deviceName(),
    prefill,
    expired,
    fromLink,
    openUrl: (url: string) => Browser.open({ url, toolbarColor: '#1c2b45' }),
    closeBrowser: () => Browser.close(),
    // upsert() has activated the new profile after a successful login: now (and only now) switch.
    ondone: () => location.reload(),
  };
}

function showConnect(target: HTMLElement, list: ServerProfile[], prefill: string, expired: boolean): (server: string, isLink: boolean) => void {
  target.replaceChildren();
  let cmp: ReturnType<typeof mount> | null = null;
  const render = (server: string, isLink: boolean) => {
    if (cmp) void unmount(cmp);
    cmp = mount(ServerConnect, { target, props: connectProps(list, server, expired && !isLink, isLink) });
  };
  render(prefill, false);
  linkHandler = (url) => promptConnect(url, (server) => render(server, true));
  void App.addListener('appUrlOpen', ({ url }) => inbound.submit({ kind: 'link', url }));
  void App.addListener('backButton', () => void App.minimizeApp());
  hideSplash();
  return render;
}

let unauthorizedOnce = false;
function onUnauthorized(): void {
  if (unauthorizedOnce || !active) return;
  unauthorizedOnce = true;
  const id = active.id;
  void (async () => {
    await profiles.markNeedsLogin(id).catch(() => {});
    await TuxMail.clearActiveSession().catch(() => {});
    writeActiveMirror(null);
    location.reload();
  })();
}

// ---------------------------------------------------------------------------
// After <App> is mounted
// ---------------------------------------------------------------------------

export async function startNative(): Promise<void> {
  if (!active) return;
  startNativeEffects({
    setBarStyle: (dark) => void SystemBars.setStyle({ style: dark ? SystemBarsStyle.Dark : SystemBarsStyle.Light }).catch(() => {}),
    cancelConversation: (id) => pendingCancels.request(id),
  });
  void installBackButton();
  void installDeepLinks();
  void installShareTarget();
  installNativeSettingsRoute();
  installOpenFileHandler();
  void autoRegisterPush();
  // Splash: wait for the first data (or give up after 2.5 s).
  const started = Date.now();
  const waitReady = () => {
    if (app.ready || app.loadError || Date.now() - started > 2500) hideSplash();
    else setTimeout(waitReady, 50);
  };
  waitReady();
}

function phoneLayout(): boolean {
  return typeof matchMedia !== 'undefined' && matchMedia('(max-width: 639px)').matches;
}

async function installBackButton(): Promise<void> {
  await App.addListener('backButton', () => {
    if (locked) return;
    const nativeSettingsOpen = location.hash.startsWith('#/native-settings');
    const chat = prefs.viewMode === 'chat';
    if (nativeSettingsOpen) {
      location.hash = '#/settings';
      return;
    }
    const action: BackAction = decideBack({
          handledByUi: dispatchBackEvent(),
          dialogOpen: !!document.querySelector('[aria-modal="true"]'),
          replying: !!app.replyingTo,
          infoOpen: app.infoOpen && chat,
          sidebarOpen: app.sidebarOpen,
          detailOpen: app.route === 'mail' && phoneLayout() && (chat ? app.selectedConversationId != null : app.selectedMessageId != null),
          route: app.route,
        });
    switch (action) {
      case 'handled':
        break;
      case 'close-dialog': {
        const dialogs = document.querySelectorAll<HTMLElement>('[aria-modal="true"]');
        dialogs[dialogs.length - 1]?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }));
        break;
      }
      case 'cancel-reply':
        app.replyingTo = null;
        break;
      case 'close-info':
        app.infoOpen = false;
        break;
      case 'close-sidebar':
        app.sidebarOpen = false;
        break;
      case 'close-conversation':
        if (chat) void app.selectConversation(null);
        else void app.selectMessage(null);
        break;
      case 'go-mail':
        app.navigate('mail');
        break;
      case 'minimize':
        void App.minimizeApp();
        break;
    }
  });
}

async function openTarget(target: DeepLinkTarget): Promise<void> {
  if (target.hash.startsWith('#/native-settings')) {
    location.hash = target.hash;
    return;
  }
  const q = new URLSearchParams(target.hash.split('?')[1] ?? '');
  if (location.hash !== target.hash.split('?')[0]) location.hash = target.hash.split('?')[0];
  if (q.get('view') === 'chat' && prefs.viewMode !== 'chat') await app.setViewMode('chat');
  // Team mailbox notifications carry #/mail?team=<id>&conversation=<id> (conversation ids of the team store).
  const teamId = Number(q.get('team'));
  if (teamId && team.activeId !== teamId) await team.enter(teamId);
  else if (!teamId && team.activeId != null) await team.leave();
  if (target.conversationId != null) await app.selectConversation(target.conversationId);
  if (target.messageId != null) await app.selectMessage(target.messageId);
  if (target.compose) app.openCompose();
}

async function installDeepLinks(): Promise<void> {
  linkHandler = (url: string) => {
    if (parseConnectLink(url)) {
      // Adding another server from a link while signed in: confirm first, keep the current session
      // until the new login has succeeded (NG-01).
      promptConnect(url, (server) => void showConnectOverlay(server));
      return;
    }
    const target = parseDeepLink(url);
    if (!target) return;
    const run = () => void openTarget(target);
    if (app.ready) run();
    else {
      const wait = () => (app.ready || app.loadError ? run() : setTimeout(wait, 100));
      wait();
    }
  };
  await App.addListener('appUrlOpen', ({ url }) => inbound.submit({ kind: 'link', url }));
  const launch = await App.getLaunchUrl().catch(() => undefined);
  if (launch?.url) inbound.submit({ kind: 'link', url: launch.url });
}

async function deliverShare(p: SharePayload): Promise<void> {
  if (!p || (!p.text && !p.subject && !p.files?.length && !p.to?.length)) return;
  const files = await sharedFilesToFiles(p, (path) => Capacitor.convertFileSrc(path.startsWith('file://') ? path : 'file://' + path));
  const detail = { text: p.text ?? '', subject: shareSubject(p), to: shareRecipients(p.to), files };
  if (dispatchShare(detail)) return;
  if (app.route !== 'mail') app.navigate('mail');
  app.openCompose({ mode: 'new', to: detail.to.map((email) => ({ email })), subject: detail.subject, bodyHtml: shareTextToHtml(detail.text) });
  if (files.length) {
    const el = await waitForElement('.composer');
    if (el) dropFiles(el, files);
  }
}

async function installShareTarget(): Promise<void> {
  shareHandler = (p) => {
    const wait = () => (app.ready || app.loadError ? void deliverShare(p) : setTimeout(wait, 100));
    wait();
  };
  await TuxMail.addListener('shareReceived', (p) => inbound.submit({ kind: 'share', payload: p }));
  const pending = await TuxMail.getPendingShare().catch(() => null);
  if (pending && (pending.text || pending.files?.length || pending.to?.length)) {
    const p: SharePayload = { text: pending.text, subject: pending.subject, to: pending.to, files: pending.files ?? [] };
    inbound.submit({ kind: 'share', payload: p });
  }
}

// ---------------------------------------------------------------------------
// Push
// ---------------------------------------------------------------------------

function pushSession(): PushSession | null {
  return active ? { serverUrl: active.serverUrl, token: activeToken, deviceName: deviceName() } : null;
}

let pushListeners = false;
let pushWaiter: ((s: PushStatus) => void) | null = null;

function settlePush(s: PushStatus): void {
  pushStatus = s;
  pushWaiter?.(s);
  pushWaiter = null;
}

async function onRegistration(reg: PushRegistration): Promise<void> {
  const session = pushSession();
  if (!session) return;
  const key = `${active?.id}|${registrationKey(reg)}`;
  const prev = await prefsStore.get(PUSH_KEY).catch(() => null);
  if (prev === key) return settlePush({ state: 'active' });
  const appId = await App.getInfo()
    .then((i) => i.id)
    .catch(() => undefined);
  const outcome = await subscribeAtServer(session, reg, appId);
  if (outcome === 'subscribed') {
    await prefsStore.set(PUSH_KEY, key).catch(() => {});
    settlePush({ state: 'active' });
  } else if (outcome === 'unsupported-by-server') settlePush({ state: 'server-unsupported' });
  else settlePush({ state: 'failed', message: outcome });
}

async function ensurePushListeners(): Promise<void> {
  if (pushListeners) return;
  pushListeners = true;
  await TuxMail.addListener('pushRegistration', (reg) => void onRegistration(reg));
  await TuxMail.addListener('pushError', (e) => settlePush({ state: e.reason === 'no_distributor' ? 'no-distributor' : e.reason === 'not_configured' ? 'not-configured' : 'failed', message: e.message }));
}

/** Ask for permission, register with FCM/UnifiedPush; resolves with the outcome. */
export async function enablePush(distributor?: string): Promise<PushStatus> {
  const session = pushSession();
  if (!session) return { state: 'failed' };
  await ensurePushListeners();
  let perm = await TuxMail.checkPermissions().catch(() => ({ notifications: 'denied' as const }));
  if (perm.notifications !== 'granted') perm = await TuxMail.requestPermissions({ permissions: ['notifications'] }).catch(() => ({ notifications: 'denied' as const }));
  if (perm.notifications !== 'granted') return settleNow({ state: 'denied' });
  const info = await TuxMail.pushInfo().catch(() => null);
  if (!info) return settleNow({ state: 'failed' });
  if (info.provider === 'fcm' && !info.configured) return settleNow({ state: 'not-configured' });
  if (info.provider === 'unifiedpush' && info.distributors.length === 0) return settleNow({ state: 'no-distributor' });
  const vapidKey = await fetchVapidKey(session);
  const result = new Promise<PushStatus>((resolve) => {
    pushWaiter = resolve;
    setTimeout(() => resolve(pushStatus.state === 'pending' ? { state: 'failed', message: 'timeout' } : pushStatus), 30000);
  });
  pushStatus = { state: 'pending' };
  try {
    await TuxMail.pushRegister({ vapidKey, distributor });
  } catch (err) {
    settlePush({ state: 'failed', message: err instanceof Error ? err.message : String(err) });
  }
  return result;
}

function settleNow(s: PushStatus): PushStatus {
  pushStatus = s;
  return s;
}

/** On every start: refresh the registration if permission was granted before (endpoints/tokens rotate). */
async function autoRegisterPush(): Promise<void> {
  const perm = await TuxMail.checkPermissions().catch(() => null);
  if (perm?.notifications !== 'granted') return;
  await enablePush();
}

// ---------------------------------------------------------------------------
// #/native-settings overlay
// ---------------------------------------------------------------------------

function controller(): NativeController {
  return {
    serverLabel: active?.label ?? '',
    userLabel: active?.displayName || active?.username || '',
    appVersion: async () => {
      const i = await App.getInfo();
      return `${i.version} (${i.build})`;
    },
    lockSettings: () => loadLockSettings(prefsStore),
    saveLockSettings: (s) => saveLockSettings(prefsStore, s),
    biometricAvailable: async () => (await TuxMail.biometricStatus().catch(() => ({ available: false }))).available,
    pushInfo: () => TuxMail.pushInfo(),
    pushStatus: () => pushStatus,
    enablePush,
    signOut: async () => {
      if (!active) return;
      await TuxMail.pushUnregister().catch(() => {});
      await prefsStore.remove(PUSH_KEY).catch(() => {});
      // Revoke the device token on the server (best effort), then forget it locally.
      await fetch(`${active.serverUrl}/api/auth/logout`, { method: 'POST', headers: { Authorization: `Bearer ${activeToken}` }, credentials: 'omit' }).catch(() => {});
      await profiles.remove(active.id);
      await TuxMail.clearActiveSession().catch(() => {});
      location.replace(location.pathname + '#/mail');
      location.reload();
    },
    switchServer: async () => {
      await profiles.deactivate();
      await TuxMail.clearActiveSession().catch(() => {});
      location.replace(location.pathname + '#/mail');
      location.reload();
    },
  };
}

function installNativeSettingsRoute(): void {
  let host: HTMLElement | null = null;
  let cmp: ReturnType<typeof mount> | null = null;
  const sync = () => {
    const open = location.hash.startsWith('#/native-settings');
    if (open && !cmp) {
      host = document.createElement('div');
      document.body.appendChild(host);
      cmp = mount(NativeSettings, { target: host, props: { ctl: controller(), onclose: () => (location.hash = '#/settings') } });
    } else if (!open && cmp) {
      void unmount(cmp);
      host?.remove();
      cmp = null;
      host = null;
    }
  };
  window.addEventListener('hashchange', sync);
  sync();
}
