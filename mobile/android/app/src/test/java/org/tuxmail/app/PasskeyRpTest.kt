// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PasskeyRpTest {
    private fun req(rpId: String?) = if (rpId == null) "{\"challenge\":\"x\"}" else "{\"rpId\":\"$rpId\"}"

    @Test
    fun acceptsHostAndParentDomain() {
        assertTrue(rpIdMatchesServer("https://mail.example.org", req("mail.example.org")))
        assertTrue(rpIdMatchesServer("https://Mail.example.org:8443/x", req("example.org")))
    }

    @Test
    fun rejectsForeignAndSuffixLikeRpIds() {
        for (rp in listOf("evil.example", "org", "xample.org", ".org", "org.", "")) {
            assertFalse(rp, rpIdMatchesServer("https://mail.example.org", req(rp)))
        }
        assertFalse(rpIdMatchesServer("https://mail.example.org", req(null)))
        assertFalse(rpIdMatchesServer("https://mail.example.org", "not json"))
        assertFalse(rpIdMatchesServer("not a url", req("example.org")))
    }
}
