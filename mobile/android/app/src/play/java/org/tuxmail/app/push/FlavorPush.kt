// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.push

import android.app.Activity
import android.content.Context
import com.getcapacitor.JSArray
import com.getcapacitor.JSObject
import com.google.firebase.FirebaseApp
import com.google.firebase.messaging.FirebaseMessaging

/**
 * play flavor: Firebase Cloud Messaging. FirebaseApp is initialised by the
 * google-services plugin from app/src/play/google-services.json; without it
 * (local/CI builds without the secret) push reports "not_configured".
 * The server sends data-only messages; [FcmService] renders them.
 */
object FlavorPush : PushBackend {
    override val provider = "fcm"

    private fun configured(context: Context): Boolean = try {
        FirebaseApp.getApps(context).isNotEmpty() || FirebaseApp.initializeApp(context) != null
    } catch (e: Exception) {
        false
    }

    override fun info(context: Context): JSObject = JSObject()
        .put("provider", provider)
        .put("configured", configured(context))
        .put("distributors", JSArray())

    override fun register(activity: Activity, vapidKey: String?, distributor: String?) {
        val ctx = activity.applicationContext
        if (!configured(ctx)) {
            PushBus.error("not_configured", "this build has no Firebase configuration")
            return
        }
        FirebaseMessaging.getInstance().isAutoInitEnabled = true
        FirebaseMessaging.getInstance().token.addOnCompleteListener { task ->
            if (task.isSuccessful && task.result != null) {
                PushBus.registration(ctx, provider, null, null, null, task.result)
            } else {
                PushBus.error("register_failed", task.exception?.message ?: "FCM token unavailable")
            }
        }
    }

    override fun unregister(context: Context) {
        if (configured(context)) FirebaseMessaging.getInstance().deleteToken()
    }
}
