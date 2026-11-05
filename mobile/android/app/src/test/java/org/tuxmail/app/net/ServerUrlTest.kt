// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.net

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** Re-audit ND-15: the stored server URL is checked by host, not by prefix. */
class ServerUrlTest {
    @Test
    fun acceptsHttpsAndLoopbackHttp() {
        for (url in listOf(
            "https://mail.example.org",
            "https://mail.example.org:8443/tuxmail",
            "http://localhost",
            "http://localhost:8080",
            "http://127.0.0.1:8080/x",
            "http://[::1]:8080",
        )) assertTrue(url, isAllowedServerUrl(url))
    }

    @Test
    fun rejectsLookalikesAndOtherSchemes() {
        for (url in listOf(
            "http://localhost.evil.example",
            "http://localhost.evil.example:8080/x",
            "http://127.0.0.1.evil.example",
            "http://localhost@evil.example",
            "http://127.0.0.1@evil.example/",
            "https://user:pw@mail.example.org",
            "http://mail.example.org",
            "ftp://mail.example.org",
            "javascript:alert(1)",
            "https://",
            "http://",
            "",
            "localhost",
            "http://local host",
        )) assertFalse(url, isAllowedServerUrl(url))
    }
}
