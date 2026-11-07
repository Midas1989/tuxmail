// SPDX-License-Identifier: AGPL-3.0-or-later
import { mount } from 'svelte';
import './app.css';
import { isNativeShell } from './lib/platform';
import { prepareDesktopBridge } from './lib/desktop/bridge';

const target = document.getElementById('app')!;

// The app state chooses its transport when it is imported: the desktop app
// first asks its core whether it works through a server (lib/desktop/bridge.ts).
async function boot(): Promise<void> {
  await prepareDesktopBridge();
  // Android app (Capacitor): server profile, login and app lock come first;
  // the native bridge is a separate chunk that never loads in browsers/Wails.
  if (isNativeShell()) {
    const [{ default: App }, n] = await Promise.all([import('./App.svelte'), import('./lib/native/capacitor')]);
    n.bootNative(target, () => {
      mount(App, { target });
      void n.startNative();
    });
    return;
  }
  // #/mail/<id>: a single mail in its own browser window (mail window "In neuem Fenster öffnen").
  const standalone = /^#\/mail\/(\d+)/.exec(location.hash);
  if (standalone) {
    const { default: StandaloneMail } = await import('./StandaloneMail.svelte');
    mount(StandaloneMail, { target, props: { id: Number(standalone[1]) } });
    return;
  }
  const { default: App } = await import('./App.svelte');
  mount(App, { target });
}

void boot();
