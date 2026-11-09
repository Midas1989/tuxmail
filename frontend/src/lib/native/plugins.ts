// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Typed handles to the native Capacitor plugins. Only @capacitor/core is a
// frontend dependency: the official plugins' native halves are installed in
// mobile/ (package.json there), and registerPlugin() talks to them by name,
// so the web bundle does not pull in their JS/web fallbacks. The interfaces
// below are the subset of the official plugin APIs (v8) that TuxMail uses.
// "TuxMail" is the app's own Kotlin plugin (mobile/android/.../TuxMailPlugin.kt).

import { registerPlugin, SystemBars, SystemBarsStyle, type PluginListenerHandle } from '@capacitor/core';

export { SystemBars, SystemBarsStyle };
export type { PluginListenerHandle };

export interface AppPlugin {
  addListener(event: 'backButton', fn: (e: { canGoBack: boolean }) => void): Promise<PluginListenerHandle>;
  addListener(event: 'appUrlOpen', fn: (e: { url: string }) => void): Promise<PluginListenerHandle>;
  addListener(event: 'appStateChange', fn: (e: { isActive: boolean }) => void): Promise<PluginListenerHandle>;
  getLaunchUrl(): Promise<{ url: string } | undefined>;
  minimizeApp(): Promise<void>;
  exitApp(): Promise<void>;
  getInfo(): Promise<{ name: string; id: string; build: string; version: string }>;
}

export interface BrowserPlugin {
  open(o: { url: string; toolbarColor?: string; presentationStyle?: 'fullscreen' | 'popover' }): Promise<void>;
  close(): Promise<void>;
  addListener(event: 'browserFinished', fn: () => void): Promise<PluginListenerHandle>;
}

export interface PreferencesPlugin {
  get(o: { key: string }): Promise<{ value: string | null }>;
  set(o: { key: string; value: string }): Promise<void>;
  remove(o: { key: string }): Promise<void>;
}

export interface SplashScreenPlugin {
  hide(o?: { fadeOutDuration?: number }): Promise<void>;
}

export interface SharePlugin {
  share(o: { title?: string; text?: string; url?: string; files?: string[]; dialogTitle?: string }): Promise<{ activityType?: string }>;
}

export interface FilesystemPlugin {
  writeFile(o: { path: string; data: string; directory?: 'CACHE' | 'DOCUMENTS' | 'DATA'; recursive?: boolean }): Promise<{ uri: string }>;
}

export interface HapticsPlugin {
  selectionChanged(): Promise<void>;
  impact(o?: { style?: 'LIGHT' | 'MEDIUM' | 'HEAVY' }): Promise<void>;
}

export interface SharedFile {
  name: string;
  mimeType: string;
  size: number;
  /** Absolute path of the copy in the app cache (convertFileSrc() makes it fetchable). */
  path: string;
}

export interface SharePayload {
  text?: string;
  subject?: string;
  /** mailto: links (ACTION_SENDTO / EXTRA_EMAIL): recipients. */
  to?: string[];
  files: SharedFile[];
}

export type PushProvider = 'fcm' | 'unifiedpush';

export interface PushRegistration {
  provider: PushProvider;
  /** UnifiedPush: Web-Push endpoint URL. */
  endpoint?: string;
  /** UnifiedPush: RFC 8291 keys of the endpoint (server encrypts like Web Push). */
  keys?: { p256dh: string; auth: string };
  /** FCM: registration token. */
  token?: string;
}

export interface PushInfo {
  provider: PushProvider;
  /** UnifiedPush: installed distributors (package names) and the selected one. */
  distributors: string[];
  distributor?: string;
  /** FCM: false when the build has no google-services.json. */
  configured: boolean;
}

export interface TuxMailPlugin {
  // Secure storage (Android Keystore AES-GCM)
  secureGet(o: { key: string }): Promise<{ value: string | null }>;
  secureSet(o: { key: string; value: string }): Promise<void>;
  secureRemove(o: { key: string }): Promise<void>;
  // Session for native notification actions (reply / mark read without WebView)
  setActiveSession(o: { serverUrl: string; token: string; profileId: string }): Promise<void>;
  clearActiveSession(): Promise<void>;
  // Biometric / device-credential app lock
  biometricStatus(): Promise<{ available: boolean; reason?: string }>;
  authenticate(o: { title: string; subtitle?: string; cancel?: string }): Promise<void>;
  // Share target
  getPendingShare(): Promise<Partial<SharePayload>>;
  addListener(event: 'shareReceived', fn: (p: SharePayload) => void): Promise<PluginListenerHandle>;
  // Push (flavor specific: play = FCM, foss = UnifiedPush)
  pushInfo(): Promise<PushInfo>;
  pushRegister(o: { vapidKey?: string; distributor?: string }): Promise<void>;
  pushUnregister(): Promise<void>;
  addListener(event: 'pushRegistration', fn: (r: PushRegistration) => void): Promise<PluginListenerHandle>;
  addListener(event: 'pushError', fn: (e: { message: string; reason?: string }) => void): Promise<PluginListenerHandle>;
  // Notifications
  checkPermissions(): Promise<{ notifications: 'granted' | 'denied' | 'prompt' | 'prompt-with-rationale' }>;
  requestPermissions(o?: { permissions?: 'notifications'[] }): Promise<{ notifications: 'granted' | 'denied' | 'prompt' | 'prompt-with-rationale' }>;
  cancelConversationNotifications(o: { conversationId: string }): Promise<void>;
  // Offline store (native/offline): network kind, background synchronisation (WorkManager), its results
  networkStatus(): Promise<{ connected: boolean; unmetered: boolean }>;
  configureOfflineSync(o: OfflineSyncConfig): Promise<void>;
  takeOfflineStaging(o: { profileId: string; limit: number }): Promise<{ entries: StagedResponse[]; more: boolean }>;
  clearOfflineStaging(): Promise<void>;
  /** A push message arrived while the app runs: time to load what is new. */
  addListener(event: 'pushReceived', fn: (p: { type?: string }) => void): Promise<PluginListenerHandle>;
}

export interface OfflineSyncConfig {
  profileId: string;
  /** 0 = no periodic work. */
  intervalMinutes: number;
  wifiOnly: boolean;
  /** mail.headersOnlyOlderThanDays; 0 = everything in full. */
  headersOnlyOlderThanDays: number;
}

/** An answer the background work loaded (JSON only). */
export interface StagedResponse {
  key: string;
  contentType?: string;
  body: string;
  storedAt?: number;
}

export const App = registerPlugin<AppPlugin>('App');
export const Browser = registerPlugin<BrowserPlugin>('Browser');
export const Preferences = registerPlugin<PreferencesPlugin>('Preferences');
export const SplashScreen = registerPlugin<SplashScreenPlugin>('SplashScreen');
export const Share = registerPlugin<SharePlugin>('Share');
export const Filesystem = registerPlugin<FilesystemPlugin>('Filesystem');
export const Haptics = registerPlugin<HapticsPlugin>('Haptics');
export const TuxMail = registerPlugin<TuxMailPlugin>('TuxMail');
