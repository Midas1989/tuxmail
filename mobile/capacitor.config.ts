// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Capacitor configuration of the TuxMail Android app (thin client, KONZEPT
// 15.9 stage A). The web layer is the regular frontend build
// (frontend/dist); the app talks to whatever TuxMail server the user enters
// on first start, so there is no `server.url` here.
//
// Overrides via environment (CI, forks, white-label builds):
//   TUXMAIL_APP_ID    application id (default org.tuxmail.app)
//   TUXMAIL_APP_NAME  launcher label (default TuxMail)

import type { CapacitorConfig } from '@capacitor/cli';

const config: CapacitorConfig = {
  appId: process.env.TUXMAIL_APP_ID || 'org.tuxmail.app',
  appName: process.env.TUXMAIL_APP_NAME || 'TuxMail',
  webDir: '../frontend/dist',
  // Never send Capacitor telemetry from builds.
  telemetry: false,
  server: {
    // https://localhost: secure context (WebCrypto, clipboard), cookies are not used.
    androidScheme: 'https',
    // Mixed content is never allowed; remote servers must use https.
    cleartext: false,
  },
  android: {
    allowMixedContent: false,
    captureInput: true,
    // WebView debugging: Capacitor default (debug builds only).
    // Release builds: the flavor is chosen by Gradle tasks (fossRelease/playRelease).
    flavor: 'foss',
  },
  plugins: {
    // Route fetch/XHR to remote servers through the native HTTP stack, so the
    // TuxMail server does not need CORS headers for the https://localhost origin.
    CapacitorHttp: { enabled: true },
    SystemBars: {
      insetsHandling: 'css',
      initialViewportFitValueHint: 'cover',
      style: 'DEFAULT',
    },
    SplashScreen: {
      // Hidden by the web layer as soon as the first screen has rendered
      // (native/capacitor.ts); the timeout is only a safety net.
      launchAutoHide: true,
      launchShowDuration: 4000,
      launchFadeOutDuration: 150,
      backgroundColor: '#f3f6fa',
      androidScaleType: 'CENTER_INSIDE',
      showSpinner: false,
    },
    Keyboard: {
      resizeOnFullScreen: true,
    },
  },
};

export default config;
