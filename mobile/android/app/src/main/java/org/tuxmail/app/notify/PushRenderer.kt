// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.notify

import android.Manifest
import android.annotation.SuppressLint
import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.Person
import androidx.core.app.RemoteInput
import androidx.core.content.ContextCompat
import org.json.JSONObject
import org.tuxmail.app.MainActivity
import org.tuxmail.app.R
import org.tuxmail.app.offline.OfflineSync
import org.tuxmail.app.push.PushBus

/**
 * Push payload as sent by the TuxMail server (internal/server/push.Payload,
 * same JSON as for the PWA service worker) plus optional fields for richer
 * native notifications (see mobile/SERVER_TODO.md).
 */
data class PushPayload(
    val type: String?,
    val title: String,
    val body: String,
    val url: String?,
    val tag: String?,
    val accountId: String?,
    val conversationId: String?,
    /** Optional: display name of the sender (chats), item id of the message. */
    val sender: String?,
    val itemId: String?,
) {
    companion object {
        fun parse(raw: String): PushPayload {
            val o = try {
                JSONObject(raw)
            } catch (e: Exception) {
                JSONObject().put("title", "TuxMail").put("body", raw.take(500))
            }
            return from(o)
        }

        fun from(o: JSONObject): PushPayload {
            fun s(k: String): String? = o.optString(k, "").takeIf { it.isNotBlank() }
            return PushPayload(
                type = s("type"),
                title = s("title") ?: "TuxMail",
                body = s("body") ?: "",
                url = s("url"),
                tag = s("tag"),
                accountId = s("accountId"),
                conversationId = s("conversationId")?.takeIf { id -> id.all { it.isDigit() } },
                sender = s("sender"),
                itemId = s("itemId"),
            )
        }
    }
}

object PushRenderer {
    const val KEY_REPLY = "tuxmail.reply"
    private const val GROUP_PREFIX = "org.tuxmail.group."

    fun render(context: Context, raw: String) {
        val p = PushPayload.parse(raw)
        render(context, p)
        // Something new at the server: the app (if running) or the background work loads it for offline reading.
        PushBus.received(p.type)
        OfflineSync.kick(context)
    }

    /** Stable id per conversation (or tag), so new messages update one notification. */
    fun notificationId(p: PushPayload): Int = when {
        p.conversationId != null -> ("conv-" + p.conversationId).hashCode()
        p.tag != null -> ("tag-" + p.tag).hashCode()
        else -> (System.currentTimeMillis() and 0x7fffffff).toInt()
    }

    fun conversationNotificationId(conversationId: String): Int = ("conv-$conversationId").hashCode()

    private fun canNotify(context: Context): Boolean {
        if (!NotificationManagerCompat.from(context).areNotificationsEnabled()) return false
        return Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
            ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
    }

    private fun active(context: Context, id: Int): Notification? =
        context.getSystemService(NotificationManager::class.java)?.activeNotifications?.firstOrNull { it.id == id }?.notification

    private fun self(context: Context): Person = Person.Builder().setName(context.getString(R.string.notification_self)).setKey("self").build()

    /** Tap target: tuxmail://open?url=<payload url>&conversationId=<id> (handled by the web layer). */
    private fun contentIntent(context: Context, p: PushPayload, requestCode: Int): PendingIntent {
        val uri = Uri.Builder().scheme("tuxmail").authority("open").apply {
            p.url?.let { appendQueryParameter("url", it) }
            p.conversationId?.let { appendQueryParameter("conversationId", it) }
        }.build()
        val intent = Intent(Intent.ACTION_VIEW, uri, context, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        return PendingIntent.getActivity(context, requestCode, intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
    }

    private fun actionIntent(context: Context, action: String, p: PushPayload, id: Int, mutable: Boolean): PendingIntent {
        val intent = Intent(context, NotificationActionReceiver::class.java)
            .setAction(action)
            .putExtra(NotificationActionReceiver.EXTRA_CONVERSATION, p.conversationId)
            .putExtra(NotificationActionReceiver.EXTRA_ITEM, p.itemId)
            .putExtra(NotificationActionReceiver.EXTRA_NOTIFICATION_ID, id)
            .putExtra(NotificationActionReceiver.EXTRA_TYPE, p.type)
        val flags = PendingIntent.FLAG_UPDATE_CURRENT or
            if (mutable && Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) PendingIntent.FLAG_MUTABLE else if (!mutable) PendingIntent.FLAG_IMMUTABLE else 0
        return PendingIntent.getBroadcast(context, id xor action.hashCode(), intent, flags)
    }

    @SuppressLint("MissingPermission") // checked in canNotify()
    fun render(context: Context, p: PushPayload) {
        Channels.ensure(context)
        if (!canNotify(context)) return
        val id = notificationId(p)
        val channel = Channels.forType(p.type)
        val group = GROUP_PREFIX + channel
        val b = NotificationCompat.Builder(context, channel)
            .setSmallIcon(R.drawable.ic_stat_tuxmail)
            .setColor(ContextCompat.getColor(context, R.color.tuxmail_orange))
            .setContentTitle(p.title)
            .setContentText(p.body)
            .setAutoCancel(true)
            .setGroup(group)
            .setContentIntent(contentIntent(context, p, id))
            .setCategory(if (channel == Channels.CHATS) NotificationCompat.CATEGORY_MESSAGE else if (channel == Channels.EVENTS) NotificationCompat.CATEGORY_EVENT else NotificationCompat.CATEGORY_EMAIL)
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setPublicVersion(
                NotificationCompat.Builder(context, channel)
                    .setSmallIcon(R.drawable.ic_stat_tuxmail)
                    .setContentTitle(context.getString(R.string.notification_public_title))
                    .build(),
            )

        if (channel == Channels.CHATS && p.conversationId != null) {
            // One notification per conversation, messages accumulate (MessagingStyle).
            val style = active(context, id)?.let { NotificationCompat.MessagingStyle.extractMessagingStyleFromNotification(it) }
                ?: NotificationCompat.MessagingStyle(self(context))
            val sender = Person.Builder().setName(p.sender ?: p.title).setKey("conv-${p.conversationId}-${p.sender ?: p.title}").build()
            if (p.sender != null && p.sender != p.title) {
                style.conversationTitle = p.title
                style.isGroupConversation = true
            }
            style.addMessage(p.body, System.currentTimeMillis(), sender)
            b.setStyle(style)
        } else if (p.body.isNotEmpty()) {
            b.setStyle(NotificationCompat.BigTextStyle().bigText(p.body))
        }

        if (p.conversationId != null && (channel == Channels.CHATS || channel == Channels.MAIL)) {
            val remote = RemoteInput.Builder(KEY_REPLY).setLabel(context.getString(R.string.notification_reply_hint)).build()
            b.addAction(
                NotificationCompat.Action.Builder(R.drawable.ic_action_reply, context.getString(R.string.notification_reply), actionIntent(context, NotificationActionReceiver.ACTION_REPLY, p, id, true))
                    .addRemoteInput(remote)
                    .setSemanticAction(NotificationCompat.Action.SEMANTIC_ACTION_REPLY)
                    .setShowsUserInterface(false)
                    .setAllowGeneratedReplies(true)
                    .build(),
            )
            b.addAction(
                NotificationCompat.Action.Builder(R.drawable.ic_action_done, context.getString(R.string.notification_mark_read), actionIntent(context, NotificationActionReceiver.ACTION_MARK_READ, p, id, false))
                    .setSemanticAction(NotificationCompat.Action.SEMANTIC_ACTION_MARK_AS_READ)
                    .setShowsUserInterface(false)
                    .build(),
            )
        }

        val nm = NotificationManagerCompat.from(context)
        nm.notify(id, b.build())
        postSummary(context, channel, group)
    }

    @SuppressLint("MissingPermission")
    private fun postSummary(context: Context, channel: String, group: String) {
        val members = context.getSystemService(NotificationManager::class.java)?.activeNotifications
            ?.filter { it.notification.group == group && (it.notification.flags and Notification.FLAG_GROUP_SUMMARY) == 0 }
            .orEmpty()
        val summaryId = group.hashCode()
        val nm = NotificationManagerCompat.from(context)
        if (members.size < 2) return
        val style = NotificationCompat.InboxStyle()
        members.take(6).forEach { sbn ->
            val title = sbn.notification.extras.getCharSequence(Notification.EXTRA_TITLE)
            if (title != null) style.addLine(title)
        }
        val summary = NotificationCompat.Builder(context, channel)
            .setSmallIcon(R.drawable.ic_stat_tuxmail)
            .setColor(ContextCompat.getColor(context, R.color.tuxmail_orange))
            .setContentTitle(context.resources.getQuantityString(R.plurals.notification_summary, members.size, members.size))
            .setStyle(style)
            .setGroup(group)
            .setGroupSummary(true)
            .setAutoCancel(true)
            .build()
        nm.notify(summaryId, summary)
    }

    /** After an inline reply: show it in the conversation (stops the "sending" spinner). */
    @SuppressLint("MissingPermission")
    fun appendOwnReply(context: Context, id: Int, text: CharSequence, failed: Boolean) {
        if (!canNotify(context)) return
        val existing = active(context, id) ?: return
        val style = NotificationCompat.MessagingStyle.extractMessagingStyleFromNotification(existing)
        val b = NotificationCompat.Builder(context, existing)
            .setOnlyAlertOnce(true)
        if (style != null) {
            style.addMessage(if (failed) context.getString(R.string.notification_reply_failed, text) else text, System.currentTimeMillis(), null as Person?)
            b.setStyle(style)
        } else {
            b.setContentText(if (failed) context.getString(R.string.notification_reply_failed, text) else text)
        }
        NotificationManagerCompat.from(context).notify(id, b.build())
    }

    fun cancelConversation(context: Context, conversationId: String) {
        NotificationManagerCompat.from(context).cancel(conversationNotificationId(conversationId))
    }
}
