// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.push

import android.app.Activity
import android.content.Context
import com.getcapacitor.JSArray
import com.getcapacitor.JSObject
import org.unifiedpush.android.connector.UnifiedPush

/**
 * foss flavor: UnifiedPush (https://unifiedpush.org) via the official
 * connector library (Maven Central, Apache-2.0). A distributor app such as
 * ntfy delivers Web-Push messages (RFC 8030); the connector decrypts them
 * (RFC 8291) with keys it generates, so the TuxMail server can keep using
 * its existing VAPID Web-Push sender.
 */
object FlavorPush : PushBackend {
    const val INSTANCE = "default"
    override val provider = "unifiedpush"

    override fun info(context: Context): JSObject {
        val distributors = UnifiedPush.getDistributors(context)
        val current = UnifiedPush.getAckDistributor(context) ?: UnifiedPush.getSavedDistributor(context)
        return JSObject()
            .put("provider", provider)
            .put("configured", true)
            .put("distributors", JSArray(distributors))
            .apply { current?.let { put("distributor", it) } }
    }

    override fun register(activity: Activity, vapidKey: String?, distributor: String?) {
        val ctx = activity.applicationContext
        if (distributor != null && distributor in UnifiedPush.getDistributors(ctx)) {
            UnifiedPush.saveDistributor(ctx, distributor)
            doRegister(ctx, vapidKey)
            return
        }
        // Uses the saved distributor, else the system default / the only one installed.
        UnifiedPush.tryUseCurrentOrDefaultDistributor(activity) { success ->
            if (success) doRegister(ctx, vapidKey) else PushBus.error("no_distributor", "no UnifiedPush distributor selected")
        }
    }

    private fun doRegister(ctx: Context, vapidKey: String?) {
        try {
            UnifiedPush.register(ctx, INSTANCE, "TuxMail", vapidKey)
        } catch (e: Exception) {
            PushBus.error("register_failed", e.message ?: e.javaClass.simpleName)
        }
    }

    override fun unregister(context: Context) {
        UnifiedPush.unregister(context, INSTANCE)
    }
}
