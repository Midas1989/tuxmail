// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app

import android.content.Intent
import android.os.Bundle
import com.getcapacitor.BridgeActivity
import org.tuxmail.app.notify.Channels
import org.tuxmail.app.share.ShareInbox

/**
 * Hosts the Capacitor WebView with the bundled TuxMail UI (frontend/dist).
 * Besides Capacitor's own handling it feeds share intents into [ShareInbox].
 * The Android 12+ splash screen is installed by @capacitor/splash-screen
 * (theme AppTheme.NoActionBarLaunch) and hidden by the web layer.
 */
class MainActivity : BridgeActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        registerPlugin(TuxMailPlugin::class.java)
        super.onCreate(savedInstanceState)
        Channels.ensure(this)
        if (savedInstanceState == null) ShareInbox.capture(this, intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        ShareInbox.capture(this, intent)
    }
}
