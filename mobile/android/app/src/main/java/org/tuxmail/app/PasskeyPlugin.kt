// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app

import android.os.Build
import android.os.CancellationSignal
import androidx.core.content.ContextCompat
import androidx.credentials.CredentialManager
import androidx.credentials.CredentialManagerCallback
import androidx.credentials.GetCredentialRequest
import androidx.credentials.GetCredentialResponse
import androidx.credentials.GetPublicKeyCredentialOption
import androidx.credentials.PublicKeyCredential
import androidx.credentials.exceptions.GetCredentialCancellationException
import androidx.credentials.exceptions.GetCredentialException
import androidx.credentials.exceptions.GetCredentialProviderConfigurationException
import androidx.credentials.exceptions.GetCredentialUnsupportedException
import androidx.credentials.exceptions.NoCredentialException
import com.getcapacitor.JSObject
import com.getcapacitor.Plugin
import com.getcapacitor.PluginCall
import com.getcapacitor.PluginMethod
import com.getcapacitor.annotation.CapacitorPlugin

/**
 * Direct passkey sign-in ("TuxMailPasskey", typed in
 * frontend/src/lib/native/passkey.ts) through Android's Credential Manager
 * (androidx.credentials). It only passes JSON through: the
 * PublicKeyCredentialRequestOptions of POST /api/device/passkey/begin go in,
 * the assertion (PublicKeyCredential JSON) comes out and is verified by the
 * server. No cryptography and no secrets in this class.
 *
 * Android hands a passkey of mail.example.org to this app only if
 * https://mail.example.org/.well-known/assetlinks.json lists the app's package
 * and signing certificate (server: auth.passkeys.android_apps). The origin in
 * clientDataJSON is then "android:apk-key-hash:<SHA-256 of the certificate>".
 *
 * play: Android 9+ through Google Play services (credentials-play-services-auth).
 * foss: Android 14+ only, through the system's credential provider framework
 * (no Google library in the F-Droid build); older devices sign in in the browser.
 */
@CapacitorPlugin(name = "TuxMailPasskey")
class PasskeyPlugin : Plugin() {
    private fun supported(): Boolean =
        if (BuildConfig.FLAVOR == "play") {
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.P
        } else {
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE
        }

    @PluginMethod
    fun isAvailable(call: PluginCall) {
        call.resolve(JSObject().put("available", supported()))
    }

    @PluginMethod
    fun getCredential(call: PluginCall) {
        val requestJson = call.getString("requestJson")
        if (requestJson.isNullOrEmpty() || requestJson.length > 64 * 1024) {
            return call.reject("invalid request", "failed")
        }
        if (!supported()) return call.reject("passkeys are not supported on this device", "unsupported")
        // The system dialog needs the activity, not the application context.
        val act = activity ?: return call.reject("no activity", "failed")
        val request = GetCredentialRequest(listOf(GetPublicKeyCredentialOption(requestJson)))
        CredentialManager.create(act).getCredentialAsync(
            act,
            request,
            CancellationSignal(),
            ContextCompat.getMainExecutor(act),
            object : CredentialManagerCallback<GetCredentialResponse, GetCredentialException> {
                override fun onResult(result: GetCredentialResponse) {
                    val cred = result.credential
                    if (cred is PublicKeyCredential) {
                        call.resolve(JSObject().put("responseJson", cred.authenticationResponseJson))
                    } else {
                        call.reject("unexpected credential type", "failed")
                    }
                }

                override fun onError(e: GetCredentialException) {
                    val code = when (e) {
                        is GetCredentialCancellationException -> "cancelled"
                        is NoCredentialException -> "no_credential"
                        is GetCredentialUnsupportedException,
                        is GetCredentialProviderConfigurationException -> "unsupported"
                        else -> "failed"
                    }
                    // The message names the reason (e.g. a missing assetlinks.json); no secrets.
                    call.reject(e.errorMessage?.toString() ?: e.type, code)
                }
            },
        )
    }
}
