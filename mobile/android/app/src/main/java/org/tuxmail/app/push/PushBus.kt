// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.push

import android.app.Activity
import android.content.Context
import android.os.Build
import android.util.Log
import com.getcapacitor.JSObject
import org.json.JSONObject
import org.tuxmail.app.net.ActiveSession
import org.tuxmail.app.net.ServerClient
import java.util.concurrent.Executors

/**
 * Flavor-specific push transport. Implementations: src/foss (UnifiedPush)
 * and src/play (Firebase Cloud Messaging), both named `FlavorPush`.
 */
interface PushBackend {
    /** "unifiedpush" or "fcm" (same as BuildConfig.PUSH_PROVIDER). */
    val provider: String

    /** {provider, distributors[], distributor?, configured} */
    fun info(context: Context): JSObject

    /** Start (or refresh) the registration; the result arrives via [PushBus]. */
    fun register(activity: Activity, vapidKey: String?, distributor: String?)

    fun unregister(context: Context)
}

/**
 * Routes registration results either to the web layer (TuxMailPlugin, which
 * then calls POST /api/push/subscribe and shows the status) or - when the
 * app is not running, e.g. FCM token rotation - registers natively with the
 * stored device token.
 */
object PushBus {
    private const val TAG = "TuxMailPush"
    private val executor = Executors.newSingleThreadExecutor()

    /** Set by TuxMailPlugin while the WebView bridge is alive. */
    @Volatile
    var sink: ((event: String, data: JSObject) -> Unit)? = null

    fun registration(context: Context, provider: String, endpoint: String?, p256dh: String?, auth: String?, token: String?) {
        val data = JSObject().put("provider", provider)
        endpoint?.let { data.put("endpoint", it) }
        token?.let { data.put("token", it) }
        if (p256dh != null && auth != null) data.put("keys", JSObject().put("p256dh", p256dh).put("auth", auth))
        val s = sink
        if (s != null) {
            s("pushRegistration", data)
            return
        }
        // Background path: same request body as frontend/src/lib/native/push.ts.
        val app = context.applicationContext
        executor.execute {
            val session = ActiveSession.load(app) ?: return@execute
            val body = JSONObject().put("type", provider).put("deviceName", "TuxMail Android (${Build.MODEL})").put("appId", app.packageName)
            endpoint?.let { body.put("endpoint", it) }
            token?.let { body.put("token", it) }
            if (p256dh != null && auth != null) body.put("keys", JSONObject().put("p256dh", p256dh).put("auth", auth))
            try {
                ServerClient.post(session, "/api/push/subscribe", body)
            } catch (e: Exception) {
                Log.w(TAG, "background push registration failed: ${e.message}")
            }
        }
    }

    fun error(reason: String, message: String) {
        Log.w(TAG, "push error $reason: $message")
        sink?.invoke("pushError", JSObject().put("reason", reason).put("message", message))
    }

    /** A push message arrived; the web layer (if alive) starts a synchronisation. */
    fun received(type: String?) {
        sink?.invoke("pushReceived", JSObject().put("type", type ?: ""))
    }

    fun unregistered(provider: String) {
        sink?.invoke("pushUnregistered", JSObject().put("provider", provider))
    }
}
