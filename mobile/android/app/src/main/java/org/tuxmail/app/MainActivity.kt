// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app

import android.content.Intent
import android.os.Bundle
import android.view.WindowManager
import com.getcapacitor.BridgeActivity
import org.json.JSONObject
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
        applySecureScreen()
        Channels.ensure(this)
        if (savedInstanceState == null) ShareInbox.capture(this, intent)
    }

    override fun onResume() {
        super.onResume()
        applySecureScreen()
    }

    override fun onPause() {
        // Before the app-switcher thumbnail is taken.
        applySecureScreen()
        super.onPause()
    }

    /**
     * With the app lock enabled the window is FLAG_SECURE: no app-switcher
     * thumbnail, screenshots or screen recording of mail content behind the
     * lock (AUDIT-D D-09). The setting is the web layer's Capacitor
     * Preferences entry "tuxmail.appLock" (frontend/src/lib/native/applock.ts).
     */
    private fun applySecureScreen() {
        val raw = getSharedPreferences("CapacitorStorage", MODE_PRIVATE).getString("tuxmail.appLock", null)
        val locked = try {
            raw != null && JSONObject(raw).optBoolean("enabled", false)
        } catch (e: Exception) {
            false
        }
        if (locked) {
            window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        } else {
            window.clearFlags(WindowManager.LayoutParams.FLAG_SECURE)
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        ShareInbox.capture(this, intent)
    }
}
