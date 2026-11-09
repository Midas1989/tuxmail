// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.offline

import android.content.Context
import android.os.Build
import android.util.Log
import androidx.work.Worker
import androidx.work.WorkerParameters
import org.json.JSONArray
import org.json.JSONObject
import org.tuxmail.app.BuildConfig
import org.tuxmail.app.net.ActiveSession
import java.net.HttpURLConnection
import java.net.URI
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/**
 * Loads, while the app is not running, what a person wants to read first
 * without a network: folders, the newest mails with their content (within
 * the headers-only limit), calendar, contacts. Same routes as the web layer's
 * synchronisation (offline/core.ts syncAll). Reading only, with the device
 * token of the active profile; a rejected token (401) ends the run quietly,
 * the app shows "Anmeldung abgelaufen" on its next start. The token is never
 * rotated here.
 */
class OfflineSyncWorker(context: Context, params: WorkerParameters) : Worker(context, params) {
    private class Rejected : Exception()

    override fun doWork(): Result {
        val config = OfflineSync.load(applicationContext) ?: return Result.success()
        val session = ActiveSession.load(applicationContext) ?: return Result.success()
        if (session.profileId != config.profileId) return Result.success()
        return try {
            run(session, config)
            Result.success()
        } catch (e: Rejected) {
            Result.success()
        } catch (e: Exception) {
            Log.w(TAG, "offline sync failed: ${e.message}")
            // A few retries with WorkManager's backoff, then wait for the next period (or push).
            if (runAttemptCount >= MAX_RETRIES) Result.success() else Result.retry()
        }
    }

    private fun run(s: ActiveSession, c: OfflineSync.Config) {
        for (p in BASE) get(s, c, p)
        get(s, c, "/api/accounts")?.let { JSONArray(it) }?.let { accounts ->
            for (i in 0 until accounts.length()) get(s, c, "/api/accounts/${accounts.getJSONObject(i).optLong("id")}/folders")
        }
        val now = System.currentTimeMillis()
        val from = iso(now - 62 * DAY)
        val to = iso(now + 366 * DAY)
        get(s, c, "/api/calendar-events?from=${enc(from)}&to=${enc(to)}")
        // mail.headersOnlyOlderThanDays: older mails keep only their header lines (the list entry).
        val cutoff = if (c.headersOnlyOlderThanDays > 0) now - c.headersOnlyOlderThanDays * DAY else null
        var details = 0
        for (list in LISTS) {
            val page = get(s, c, list)?.let { JSONObject(it) } ?: continue
            val items = page.optJSONArray("items") ?: continue
            for (i in 0 until items.length()) {
                if (isStopped || details >= MAX_DETAILS) return
                val m = items.getJSONObject(i)
                val date = parse(m.optString("date"))
                if (cutoff != null && date != null && date < cutoff) continue
                val key = "/api/messages/${m.optLong("id")}"
                if (OfflineSync.has(applicationContext, c.profileId, key)) continue
                get(s, c, key)
                details++
            }
        }
    }

    /** GET a route; keeps a 200 JSON answer in the staging folder. */
    private fun get(s: ActiveSession, c: OfflineSync.Config, path: String): String? {
        if (isStopped) return null
        val conn = URI(s.serverUrl + path).toURL().openConnection() as HttpURLConnection
        try {
            conn.connectTimeout = 15_000
            conn.readTimeout = 30_000
            conn.instanceFollowRedirects = false
            conn.setRequestProperty("Authorization", "Bearer ${s.token}")
            conn.setRequestProperty("Accept", "application/json")
            conn.setRequestProperty("User-Agent", "TuxMail-Android/${BuildConfig.VERSION_NAME} (Android ${Build.VERSION.RELEASE})")
            val status = conn.responseCode
            if (status == 401) throw Rejected()
            if (status != 200) return null
            val ct = conn.contentType ?: "application/json"
            if (!ct.contains("json")) return null
            val body = conn.inputStream.use { it.readBytes() }
            if (body.size > MAX_BODY) return null
            val text = String(body, Charsets.UTF_8)
            OfflineSync.stage(applicationContext, c.profileId, path, ct, text)
            return text
        } finally {
            conn.disconnect()
        }
    }

    private fun enc(v: String) = java.net.URLEncoder.encode(v, "UTF-8")

    private fun fmt() = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss", Locale.US).apply { timeZone = TimeZone.getTimeZone("UTC") }

    private fun iso(ms: Long) = fmt().format(Date(ms)) + "Z"

    /** Day precision is enough: the offset of an RFC 3339 date is ignored (minSdk 24 has no java.time). */
    private fun parse(v: String): Long? = try {
        if (v.length < 19) null else fmt().parse(v.substring(0, 19))?.time
    } catch (_: Exception) {
        null
    }

    companion object {
        private const val TAG = "TuxMailOffline"
        private const val DAY = 24L * 60 * 60 * 1000
        private const val MAX_BODY = 4 * 1024 * 1024
        private const val MAX_DETAILS = 60
        private const val MAX_RETRIES = 3
        private val BASE = listOf(
            "/api/auth/me", "/api/identities", "/api/signatures", "/api/persons", "/api/preferences", "/api/tags",
            "/api/calendars", "/api/tasks", "/api/addressbooks", "/api/contacts", "/api/contact-groups", "/api/chat-folders",
        )
        private val LISTS = listOf(
            "/api/messages?scope=unified&role=inbox&offset=0&limit=100",
            "/api/messages?scope=unified&role=sent&offset=0&limit=50",
        )
    }
}
