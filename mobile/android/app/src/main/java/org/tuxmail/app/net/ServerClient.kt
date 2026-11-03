// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.net

import android.content.Context
import android.os.Build
import org.json.JSONObject
import org.tuxmail.app.BuildConfig
import org.tuxmail.app.security.SecureStore
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URI

/** Server URL + device token of the active profile (mirrored from the web layer). */
data class ActiveSession(val serverUrl: String, val token: String, val profileId: String) {
    fun toJson(): String = JSONObject().put("serverUrl", serverUrl).put("token", token).put("profileId", profileId).toString()

    companion object {
        fun load(context: Context): ActiveSession? {
            val raw = SecureStore(context).get(SecureStore.ACTIVE_SESSION) ?: return null
            return try {
                val o = JSONObject(raw)
                val url = o.getString("serverUrl")
                if (!url.startsWith("https://") && !url.startsWith("http://localhost") && !url.startsWith("http://127.0.0.1")) return null
                ActiveSession(url.trimEnd('/'), o.getString("token"), o.optString("profileId"))
            } catch (e: Exception) {
                null
            }
        }

        fun save(context: Context, session: ActiveSession) = SecureStore(context).put(SecureStore.ACTIVE_SESSION, session.toJson())

        fun clear(context: Context) = SecureStore(context).remove(SecureStore.ACTIVE_SESSION)
    }
}

/**
 * Minimal JSON client for the few calls made without the WebView
 * (notification actions, push re-registration in the background).
 */
object ServerClient {
    private const val TIMEOUT_MS = 10_000

    class HttpError(val status: Int) : IOException("HTTP $status")

    /** POST [body] to [path]; returns the HTTP status (2xx) or throws. */
    @Throws(IOException::class)
    fun post(session: ActiveSession, path: String, body: JSONObject): Int = send(session, "POST", path, body)

    @Throws(IOException::class)
    fun send(session: ActiveSession, method: String, path: String, body: JSONObject?): Int {
        val conn = URI(session.serverUrl + path).toURL().openConnection() as HttpURLConnection
        try {
            conn.requestMethod = method
            conn.connectTimeout = TIMEOUT_MS
            conn.readTimeout = TIMEOUT_MS
            conn.instanceFollowRedirects = false
            conn.setRequestProperty("Authorization", "Bearer ${session.token}")
            conn.setRequestProperty("Accept", "application/json")
            conn.setRequestProperty("User-Agent", "TuxMail-Android/${BuildConfig.VERSION_NAME} (Android ${Build.VERSION.RELEASE})")
            if (body != null) {
                conn.doOutput = true
                conn.setRequestProperty("Content-Type", "application/json")
                conn.outputStream.use { it.write(body.toString().toByteArray(Charsets.UTF_8)) }
            }
            val status = conn.responseCode
            (if (status >= 400) conn.errorStream else conn.inputStream)?.use { it.readBytes() }
            if (status !in 200..299) throw HttpError(status)
            return status
        } finally {
            conn.disconnect()
        }
    }
}
