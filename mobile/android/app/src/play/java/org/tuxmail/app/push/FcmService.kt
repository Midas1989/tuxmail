// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.push

import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import org.json.JSONObject
import org.tuxmail.app.notify.PushRenderer

/**
 * FCM receiver. Expects data-only messages whose keys mirror the Web-Push
 * payload (type, title, body, url, tag, accountId, conversationId, sender,
 * itemId) - or a single "payload" key holding that JSON. See SERVER_TODO.md.
 */
class FcmService : FirebaseMessagingService() {
    override fun onNewToken(token: String) {
        PushBus.registration(this, "fcm", null, null, null, token)
    }

    override fun onMessageReceived(message: RemoteMessage) {
        val data = message.data
        val n = message.notification
        if (data.isEmpty() && n != null) {
            PushRenderer.render(this, JSONObject().put("title", n.title ?: "TuxMail").put("body", n.body ?: "").toString())
            return
        }
        PushRenderer.render(this, data["payload"] ?: JSONObject(data.toMap()).toString())
    }
}
