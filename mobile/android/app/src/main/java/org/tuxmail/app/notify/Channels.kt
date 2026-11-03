// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.notify

import android.content.Context
import androidx.core.app.NotificationChannelCompat
import androidx.core.app.NotificationManagerCompat
import org.tuxmail.app.R

/**
 * Notification channels (user-configurable per channel in the Android
 * settings): Mail, Chats, Termine (calendar), Erinnerungen (reminders,
 * "Warten auf Antwort", workflow and system messages).
 */
object Channels {
    const val MAIL = "tuxmail_mail"
    const val CHATS = "tuxmail_chats"
    const val EVENTS = "tuxmail_events"
    const val REMINDERS = "tuxmail_reminders"

    fun ensure(context: Context) {
        val nm = NotificationManagerCompat.from(context)
        val list = listOf(
            NotificationChannelCompat.Builder(MAIL, NotificationManagerCompat.IMPORTANCE_DEFAULT)
                .setName(context.getString(R.string.channel_mail))
                .setDescription(context.getString(R.string.channel_mail_desc))
                .build(),
            NotificationChannelCompat.Builder(CHATS, NotificationManagerCompat.IMPORTANCE_HIGH)
                .setName(context.getString(R.string.channel_chats))
                .setDescription(context.getString(R.string.channel_chats_desc))
                .build(),
            NotificationChannelCompat.Builder(EVENTS, NotificationManagerCompat.IMPORTANCE_HIGH)
                .setName(context.getString(R.string.channel_events))
                .setDescription(context.getString(R.string.channel_events_desc))
                .build(),
            NotificationChannelCompat.Builder(REMINDERS, NotificationManagerCompat.IMPORTANCE_DEFAULT)
                .setName(context.getString(R.string.channel_reminders))
                .setDescription(context.getString(R.string.channel_reminders_desc))
                .build(),
        )
        nm.createNotificationChannelsCompat(list)
    }

    /** Payload "type" (internal/server/push.Payload) -> channel id. */
    fun forType(type: String?): String = when (type) {
        "mail" -> MAIL
        "chat", "message" -> CHATS
        "calendar", "event", "invitation" -> EVENTS
        else -> REMINDERS
    }
}
