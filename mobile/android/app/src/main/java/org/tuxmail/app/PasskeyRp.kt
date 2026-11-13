// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app

import org.json.JSONObject
import java.net.URI
import java.util.Locale

/**
 * Every assertion of this app carries the same origin (android:apk-key-hash), so
 * the relying party must be pinned to the server the user connected to: rpId is
 * that server's host or a parent domain of it (a bare public suffix has no dot).
 * Mirrors rpIdMatchesServer in frontend/src/lib/native/passkey.ts.
 */
internal fun rpIdMatchesServer(serverUrl: String, requestJson: String): Boolean {
    val rpId = try {
        JSONObject(requestJson).optString("rpId", "").lowercase(Locale.ROOT)
    } catch (_: Exception) {
        return false
    }
    val host = try {
        URI(serverUrl).host?.lowercase(Locale.ROOT)
    } catch (_: Exception) {
        null
    }
    if (rpId.isEmpty() || host.isNullOrEmpty()) return false
    if (host == rpId) return true
    return rpId.contains('.') && !rpId.startsWith('.') && !rpId.endsWith('.') && host.endsWith(".$rpId")
}
