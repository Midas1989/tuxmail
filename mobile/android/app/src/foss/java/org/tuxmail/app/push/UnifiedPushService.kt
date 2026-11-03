// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.push

import org.tuxmail.app.notify.PushRenderer
import org.unifiedpush.android.connector.FailedReason
import org.unifiedpush.android.connector.PushService
import org.unifiedpush.android.connector.data.PushEndpoint
import org.unifiedpush.android.connector.data.PushMessage

/** Receives UnifiedPush events from the distributor (declared in src/foss/AndroidManifest.xml). */
class UnifiedPushService : PushService() {
    override fun onNewEndpoint(endpoint: PushEndpoint, instance: String) {
        val keys = endpoint.pubKeySet
        PushBus.registration(this, "unifiedpush", endpoint.url, keys?.pubKey, keys?.auth, null)
    }

    override fun onMessage(message: PushMessage, instance: String) {
        // Decrypted by the connector when the endpoint has keys (always, since we registered with VAPID).
        PushRenderer.render(this, String(message.content, Charsets.UTF_8))
    }

    override fun onRegistrationFailed(reason: FailedReason, instance: String) {
        PushBus.error(reason.name.lowercase(), "UnifiedPush registration failed: ${reason.name}")
    }

    override fun onUnregistered(instance: String) {
        PushBus.unregistered("unifiedpush")
    }
}
