// SPDX-License-Identifier: AGPL-3.0-or-later
import { mount } from 'svelte';
import './app.css';
import App from './App.svelte';
import StandaloneMail from './StandaloneMail.svelte';
import { isNativeShell } from './lib/platform';

const target = document.getElementById('app')!;

// Android app (Capacitor): server profile, login and app lock come first;
// the native bridge is a separate chunk that never loads in browsers/Wails.
if (isNativeShell()) {
  void import('./lib/native/capacitor').then((n) =>
    n.bootNative(target, () => {
      mount(App, { target });
      void n.startNative();
    }),
  );
}

// #/mail/<id>: a single mail in its own browser window (mail window "In neuem Fenster öffnen").
const standalone = /^#\/mail\/(\d+)/.exec(location.hash);
const app = isNativeShell() ? undefined : standalone ? mount(StandaloneMail, { target, props: { id: Number(standalone[1]) } }) : mount(App, { target });

export default app;
