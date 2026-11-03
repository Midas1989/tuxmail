// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.notify

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.RemoteInput
import org.json.JSONObject
import org.tuxmail.app.net.ActiveSession
import org.tuxmail.app.net.ServerClient
import java.util.concurrent.Executors

/**
 * Notification actions that work without opening the app:
 *  - inline reply  -> POST /api/conversations/{id}/reply {text, replyToItemId?}
 *  - mark as read  -> POST /api/conversations/{id}/read
 * Authenticated with the device token of the active profile (SecureStore).
 * The reply endpoint is not implemented by the server yet, see mobile/SERVER_TODO.md.
 */
class NotificationActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val conversationId = intent.getStringExtra(EXTRA_CONVERSATION)?.takeIf { id -> id.isNotEmpty() && id.all { it.isDigit() } } ?: return
        val notificationId = intent.getIntExtra(EXTRA_NOTIFICATION_ID, PushRenderer.conversationNotificationId(conversationId))
        val app = context.applicationContext
        val pending = goAsync()
        executor.execute {
            try {
                when (intent.action) {
                    ACTION_REPLY -> reply(app, intent, conversationId, notificationId)
                    ACTION_MARK_READ -> markRead(app, conversationId, notificationId)
                }
            } finally {
                pending.finish()
            }
        }
    }

    private fun reply(context: Context, intent: Intent, conversationId: String, notificationId: Int) {
        val text = RemoteInput.getResultsFromIntent(intent)?.getCharSequence(PushRenderer.KEY_REPLY)?.toString()?.trim()
        if (text.isNullOrEmpty()) return
        val session = ActiveSession.load(context)
        val ok = session != null && try {
            val body = JSONObject().put("text", text)
            intent.getStringExtra(EXTRA_ITEM)?.let { body.put("replyToItemId", it) }
            ServerClient.post(session, "/api/conversations/$conversationId/reply", body)
            true
        } catch (e: Exception) {
            Log.w(TAG, "inline reply failed: ${e.message}")
            false
        }
        PushRenderer.appendOwnReply(context, notificationId, text, failed = !ok)
    }

    private fun markRead(context: Context, conversationId: String, notificationId: Int) {
        val session = ActiveSession.load(context) ?: return
        try {
            ServerClient.post(session, "/api/conversations/$conversationId/read", JSONObject())
            NotificationManagerCompat.from(context).cancel(notificationId)
        } catch (e: Exception) {
            Log.w(TAG, "mark as read failed: ${e.message}")
        }
    }

    companion object {
        private const val TAG = "TuxMailNotify"
        const val ACTION_REPLY = "org.tuxmail.app.action.REPLY"
        const val ACTION_MARK_READ = "org.tuxmail.app.action.MARK_READ"
        const val EXTRA_CONVERSATION = "conversationId"
        const val EXTRA_ITEM = "itemId"
        const val EXTRA_NOTIFICATION_ID = "notificationId"
        const val EXTRA_TYPE = "type"
        private val executor = Executors.newSingleThreadExecutor()
    }
}
