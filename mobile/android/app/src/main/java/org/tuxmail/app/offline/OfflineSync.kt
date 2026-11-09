// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.offline

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/**
 * Background synchronisation of the offline store (frontend/src/lib/native/offline).
 * The store itself lives in the WebView (IndexedDB), which Android work cannot
 * reach: [OfflineSyncWorker] loads the server's answers into a staging folder
 * (app-private, no backup) and the web layer takes them over on its next
 * start/resume (TuxMailPlugin.takeOfflineStaging). Only reading routes, never
 * sending: mails leave from the outbox while the app runs.
 */
object OfflineSync {
    private const val PREFS = "tuxmail.offline"
    private const val PERIODIC = "tuxmail.offline.periodic"
    private const val ONCE = "tuxmail.offline.once"
    /** Upper limit of the staging folder; older entries go first. */
    const val STAGING_MAX_BYTES = 20L * 1024 * 1024

    data class Config(val profileId: String, val intervalMinutes: Int, val wifiOnly: Boolean, val headersOnlyOlderThanDays: Int)

    fun load(context: Context): Config? {
        val p = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val id = p.getString("profileId", null) ?: return null
        return Config(id, p.getInt("intervalMinutes", 0), p.getBoolean("wifiOnly", false), p.getInt("headersOnlyOlderThanDays", 0))
    }

    fun configure(context: Context, c: Config) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putString("profileId", c.profileId)
            .putInt("intervalMinutes", c.intervalMinutes)
            .putBoolean("wifiOnly", c.wifiOnly)
            .putInt("headersOnlyOlderThanDays", c.headersOnlyOlderThanDays)
            .apply()
        val wm = WorkManager.getInstance(context)
        if (c.intervalMinutes <= 0) {
            wm.cancelUniqueWork(PERIODIC)
            return
        }
        val req = PeriodicWorkRequestBuilder<OfflineSyncWorker>(maxOf(15L, c.intervalMinutes.toLong()), TimeUnit.MINUTES)
            .setConstraints(constraints(c))
            .build()
        wm.enqueueUniquePeriodicWork(PERIODIC, ExistingPeriodicWorkPolicy.UPDATE, req)
    }

    /** A push message arrived: load what is new soon (same conditions as the periodic work). */
    fun kick(context: Context) {
        val c = load(context) ?: return
        if (c.intervalMinutes <= 0) return
        val req = OneTimeWorkRequestBuilder<OfflineSyncWorker>().setConstraints(constraints(c)).build()
        WorkManager.getInstance(context).enqueueUniqueWork(ONCE, ExistingWorkPolicy.KEEP, req)
    }

    /** Sign-out, server switch: nothing loaded in the background stays. */
    fun stop(context: Context) {
        val wm = WorkManager.getInstance(context)
        wm.cancelUniqueWork(PERIODIC)
        wm.cancelUniqueWork(ONCE)
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().clear().apply()
        clearStaging(context)
    }

    private fun constraints(c: Config) = Constraints.Builder()
        .setRequiredNetworkType(if (c.wifiOnly) NetworkType.UNMETERED else NetworkType.CONNECTED)
        .setRequiresBatteryNotLow(true)
        .build()

    // ---- network state for the web layer --------------------------------------------------

    fun networkStatus(context: Context): JSONObject {
        val cm = context.getSystemService(ConnectivityManager::class.java)
        val caps = cm?.getNetworkCapabilities(cm.activeNetwork)
        val connected = caps?.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) == true
        val unmetered = caps?.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) == true
        return JSONObject().put("connected", connected).put("unmetered", unmetered)
    }

    // ---- staging -------------------------------------------------------------------------------

    private fun dir(context: Context, profileId: String) = File(context.noBackupFilesDir, "offline-staging/" + profileId.replace(Regex("[^A-Za-z0-9_-]"), "_"))

    private fun name(key: String): String {
        val d = MessageDigest.getInstance("SHA-256").digest(key.toByteArray(Charsets.UTF_8))
        return d.joinToString("") { "%02x".format(it) } + ".json"
    }

    fun has(context: Context, profileId: String, key: String) = File(dir(context, profileId), name(key)).exists()

    /** Keeps one answer; the newest answer of a route replaces the older one. */
    @Synchronized
    fun stage(context: Context, profileId: String, key: String, contentType: String, body: String) {
        val d = dir(context, profileId)
        d.mkdirs()
        val entry = JSONObject().put("key", key).put("contentType", contentType).put("body", body).put("storedAt", System.currentTimeMillis())
        File(d, name(key)).writeText(entry.toString(), Charsets.UTF_8)
        var files = d.listFiles()?.sortedBy { it.lastModified() } ?: return
        var total = files.sumOf { it.length() }
        while (total > STAGING_MAX_BYTES && files.isNotEmpty()) {
            total -= files.first().length()
            files.first().delete()
            files = files.drop(1)
        }
    }

    /** Hands up to [limit] staged answers to the web layer and deletes them. */
    @Synchronized
    fun take(context: Context, profileId: String, limit: Int): Pair<JSONArray, Boolean> {
        val files = dir(context, profileId).listFiles()?.sortedBy { it.lastModified() } ?: return JSONArray() to false
        val out = JSONArray()
        for (f in files.take(limit)) {
            try {
                out.put(JSONObject(f.readText(Charsets.UTF_8)))
            } catch (_: Exception) {
                // damaged: drop it
            }
            f.delete()
        }
        return out to (files.size > limit)
    }

    fun clearStaging(context: Context) {
        File(context.noBackupFilesDir, "offline-staging").deleteRecursively()
    }
}
