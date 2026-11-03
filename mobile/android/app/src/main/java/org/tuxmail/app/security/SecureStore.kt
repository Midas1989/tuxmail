// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app.security

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Small secret store for device tokens: values are encrypted with an
 * AES-256-GCM key that lives in the Android Keystore (non-exportable,
 * hardware-backed where available) and stored in a private
 * SharedPreferences file. The entry name is bound as AAD, so ciphertexts
 * cannot be swapped between keys.
 *
 * Deliberately not androidx.security:security-crypto (EncryptedSharedPreferences
 * is deprecated) and no third-party plugin.
 */
class SecureStore(context: Context) {
    private val prefs = context.applicationContext.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    @Synchronized
    fun get(name: String): String? {
        val stored = prefs.getString(name, null) ?: return null
        return try {
            val raw = Base64.decode(stored, Base64.NO_WRAP)
            require(raw.size > 1 + IV_LEN && raw[0] == VERSION)
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, raw, 1, IV_LEN))
            cipher.updateAAD(name.toByteArray(Charsets.UTF_8))
            String(cipher.doFinal(raw, 1 + IV_LEN, raw.size - 1 - IV_LEN), Charsets.UTF_8)
        } catch (e: Exception) {
            // Key invalidated (e.g. device restored) or data corrupt: forget the entry.
            prefs.edit().remove(name).apply()
            null
        }
    }

    @Synchronized
    fun put(name: String, value: String) {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        cipher.updateAAD(name.toByteArray(Charsets.UTF_8))
        val ct = cipher.doFinal(value.toByteArray(Charsets.UTF_8))
        val iv = cipher.iv
        check(iv.size == IV_LEN)
        val out = ByteArray(1 + iv.size + ct.size)
        out[0] = VERSION
        System.arraycopy(iv, 0, out, 1, iv.size)
        System.arraycopy(ct, 0, out, 1 + iv.size, ct.size)
        prefs.edit().putString(name, Base64.encodeToString(out, Base64.NO_WRAP)).apply()
    }

    @Synchronized
    fun remove(name: String) {
        prefs.edit().remove(name).apply()
    }

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance(KEYSTORE).apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE)
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .setRandomizedEncryptionRequired(true)
                .build(),
        )
        return gen.generateKey()
    }

    companion object {
        private const val PREFS = "tuxmail_secure"
        private const val KEYSTORE = "AndroidKeyStore"
        private const val ALIAS = "tuxmail.secure.v1"
        private const val TRANSFORMATION = "AES/GCM/NoPadding"
        private const val IV_LEN = 12
        private const val TAG_BITS = 128
        private const val VERSION: Byte = 1

        /** Session used by notification actions (reply / mark read) while the WebView is not running. */
        const val ACTIVE_SESSION = "native.activeSession"
    }
}
