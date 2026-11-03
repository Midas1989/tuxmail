// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.share

import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.provider.OpenableColumns
import android.util.Log
import com.getcapacitor.JSArray
import com.getcapacitor.JSObject
import java.io.File
import java.util.UUID
import java.util.concurrent.Executors

/**
 * Receives ACTION_SEND / ACTION_SEND_MULTIPLE / ACTION_SENDTO(mailto:)
 * intents. Shared content URIs are copied into cache/shared/<uuid>/ so the
 * WebView can read them (Capacitor.convertFileSrc). Only content:// URIs are
 * accepted: file:// URIs from other apps could point into TuxMail's private
 * data directory and leak it as an "attachment".
 */
object ShareInbox {
    private const val TAG = "TuxMailShare"
    private const val MAX_FILES = 20
    private const val MAX_BYTES = 200L * 1024 * 1024
    private const val KEEP_MS = 24L * 60 * 60 * 1000

    private val executor = Executors.newSingleThreadExecutor()
    private var pending: JSObject? = null

    /** Set by TuxMailPlugin while the bridge is alive; returns true if delivered. */
    @Volatile
    var sink: ((JSObject) -> Boolean)? = null

    @Synchronized
    fun takePending(): JSObject? = pending.also { pending = null }

    @Synchronized
    private fun deliver(payload: JSObject) {
        if (sink?.invoke(payload) != true) pending = payload
    }

    fun capture(context: Context, intent: Intent?) {
        intent ?: return
        val action = intent.action ?: return
        if (action != Intent.ACTION_SEND && action != Intent.ACTION_SEND_MULTIPLE && action != Intent.ACTION_SENDTO) return
        val app = context.applicationContext
        val text = intent.getCharSequenceExtra(Intent.EXTRA_TEXT)?.toString()
        val subject = intent.getStringExtra(Intent.EXTRA_SUBJECT)
        val to = mutableListOf<String>()
        intent.getStringArrayExtra(Intent.EXTRA_EMAIL)?.let { to += it }
        if (action == Intent.ACTION_SENDTO) intent.data?.let { to += mailtoRecipients(it) }
        val uris = streams(intent)
        // Consume the intent so a configuration change does not share twice.
        intent.action = null
        executor.execute {
            cleanup(app)
            val files = JSArray()
            if (uris.isNotEmpty()) {
                val dir = File(app.cacheDir, "shared/" + UUID.randomUUID()).apply { mkdirs() }
                var total = 0L
                for (uri in uris.take(MAX_FILES)) {
                    val f = copy(app.contentResolver, uri, dir, MAX_BYTES - total) ?: continue
                    total += f.getLong("size")
                    files.put(f)
                }
            }
            val payload = JSObject()
            text?.let { payload.put("text", it.take(100_000)) }
            subject?.let { payload.put("subject", it.take(500)) }
            if (to.isNotEmpty()) payload.put("to", JSArray(to.take(50)))
            payload.put("files", files)
            if (text != null || subject != null || to.isNotEmpty() || files.length() > 0) deliver(payload)
        }
    }

    private fun streams(intent: Intent): List<Uri> {
        val out = mutableListOf<Uri>()
        if (intent.action == Intent.ACTION_SEND_MULTIPLE) {
            val list = if (Build.VERSION.SDK_INT >= 33) {
                intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java)
            } else {
                @Suppress("DEPRECATION")
                intent.getParcelableArrayListExtra<Uri>(Intent.EXTRA_STREAM)
            }
            list?.let { out += it }
        } else {
            val one = if (Build.VERSION.SDK_INT >= 33) {
                intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java)
            } else {
                @Suppress("DEPRECATION")
                intent.getParcelableExtra<Uri>(Intent.EXTRA_STREAM)
            }
            one?.let { out += it }
        }
        if (out.isEmpty()) intent.clipData?.let { clip -> for (i in 0 until clip.itemCount) clip.getItemAt(i).uri?.let { out += it } }
        return out.filter { it.scheme == ContentResolver.SCHEME_CONTENT }.distinct()
    }

    internal fun mailtoRecipients(uri: Uri): List<String> {
        if (uri.scheme != "mailto") return emptyList()
        val raw = uri.schemeSpecificPart?.substringBefore('?') ?: return emptyList()
        return raw.split(',', ';').map { it.trim() }.filter { it.contains('@') && it.length < 320 }
    }

    internal fun safeName(name: String?): String {
        val base = (name ?: "").substringAfterLast('/').substringAfterLast('\\')
            .replace(Regex("[\\u0000-\\u001f<>:\"|?*]"), "_")
            .trim().trimStart('.')
        return base.take(120).ifEmpty { "file" }
    }

    private fun copy(resolver: ContentResolver, uri: Uri, dir: File, budget: Long): JSObject? {
        return try {
            var name: String? = null
            var size = -1L
            resolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use { c ->
                if (c.moveToFirst()) {
                    val ni = c.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                    val si = c.getColumnIndex(OpenableColumns.SIZE)
                    if (ni >= 0) name = c.getString(ni)
                    if (si >= 0 && !c.isNull(si)) size = c.getLong(si)
                }
            }
            if (size > budget) return null
            var target = File(dir, safeName(name))
            var n = 1
            while (target.exists()) target = File(dir, "${n++}-" + safeName(name))
            var copied = 0L
            resolver.openInputStream(uri)?.use { input ->
                target.outputStream().use { out ->
                    val buf = ByteArray(64 * 1024)
                    while (true) {
                        val r = input.read(buf)
                        if (r < 0) break
                        copied += r
                        if (copied > budget) throw IllegalStateException("share too large")
                        out.write(buf, 0, r)
                    }
                }
            } ?: return null
            JSObject()
                .put("name", target.name)
                .put("mimeType", resolver.getType(uri) ?: "application/octet-stream")
                .put("size", copied)
                .put("path", target.absolutePath)
        } catch (e: Exception) {
            Log.w(TAG, "could not copy shared item: ${e.message}")
            null
        }
    }

    /** Remove share copies older than a day. */
    private fun cleanup(context: Context) {
        val root = File(context.cacheDir, "shared")
        val cutoff = System.currentTimeMillis() - KEEP_MS
        root.listFiles()?.filter { it.lastModified() < cutoff }?.forEach { it.deleteRecursively() }
    }
}
