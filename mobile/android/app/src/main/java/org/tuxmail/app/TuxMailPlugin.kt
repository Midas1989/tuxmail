// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors

package org.tuxmail.app

import android.Manifest
import android.os.Build
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_WEAK
import androidx.biometric.BiometricManager.Authenticators.DEVICE_CREDENTIAL
import androidx.biometric.BiometricPrompt
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import com.getcapacitor.JSObject
import com.getcapacitor.PermissionState
import com.getcapacitor.Plugin
import com.getcapacitor.PluginCall
import com.getcapacitor.PluginMethod
import com.getcapacitor.annotation.CapacitorPlugin
import com.getcapacitor.annotation.Permission
import com.getcapacitor.annotation.PermissionCallback
import org.tuxmail.app.net.ActiveSession
import org.tuxmail.app.notify.Channels
import org.tuxmail.app.notify.PushRenderer
import org.tuxmail.app.push.FlavorPush
import org.tuxmail.app.push.PushBus
import org.tuxmail.app.security.SecureStore
import org.tuxmail.app.share.ShareInbox

/**
 * The app's own Capacitor plugin ("TuxMail", typed in
 * frontend/src/lib/native/plugins.ts): Keystore-backed secure storage,
 * biometric app lock, share target, flavor-specific push and notification
 * housekeeping.
 */
@CapacitorPlugin(
    name = "TuxMail",
    permissions = [Permission(alias = "notifications", strings = [Manifest.permission.POST_NOTIFICATIONS])],
)
class TuxMailPlugin : Plugin() {
    private lateinit var store: SecureStore

    override fun load() {
        store = SecureStore(context)
        Channels.ensure(context)
        ShareInbox.sink = { payload ->
            if (hasListeners("shareReceived")) {
                notifyListeners("shareReceived", payload)
                true
            } else {
                false
            }
        }
        PushBus.sink = { event, data -> notifyListeners(event, data, event == "pushRegistration") }
    }

    override fun handleOnDestroy() {
        ShareInbox.sink = null
        PushBus.sink = null
        super.handleOnDestroy()
    }

    // ---- secure storage ---------------------------------------------------

    private fun key(call: PluginCall): String? {
        val k = call.getString("key")
        if (k.isNullOrEmpty() || k.length > 200 || k == SecureStore.ACTIVE_SESSION) {
            call.reject("invalid key", "invalid_key")
            return null
        }
        return k
    }

    @PluginMethod
    fun secureGet(call: PluginCall) {
        val k = key(call) ?: return
        call.resolve(JSObject().put("value", store.get(k) ?: JSObject.NULL))
    }

    @PluginMethod
    fun secureSet(call: PluginCall) {
        val k = key(call) ?: return
        val v = call.getString("value") ?: return call.reject("missing value", "invalid_value")
        try {
            store.put(k, v)
            call.resolve()
        } catch (e: Exception) {
            call.reject("secure storage unavailable", "storage_error", e)
        }
    }

    @PluginMethod
    fun secureRemove(call: PluginCall) {
        val k = key(call) ?: return
        store.remove(k)
        call.resolve()
    }

    @PluginMethod
    fun setActiveSession(call: PluginCall) {
        val url = call.getString("serverUrl")
        val token = call.getString("token")
        if (url.isNullOrEmpty() || token.isNullOrEmpty()) return call.reject("serverUrl and token required", "invalid_session")
        ActiveSession.save(context, ActiveSession(url.trimEnd('/'), token, call.getString("profileId") ?: ""))
        call.resolve()
    }

    @PluginMethod
    fun clearActiveSession(call: PluginCall) {
        ActiveSession.clear(context)
        NotificationManagerCompat.from(context).cancelAll()
        call.resolve()
    }

    // ---- biometric app lock -------------------------------------------------

    private val authenticators = BIOMETRIC_WEAK or DEVICE_CREDENTIAL

    @PluginMethod
    fun biometricStatus(call: PluginCall) {
        val r = BiometricManager.from(context).canAuthenticate(authenticators)
        val out = JSObject().put("available", r == BiometricManager.BIOMETRIC_SUCCESS)
        when (r) {
            BiometricManager.BIOMETRIC_ERROR_NONE_ENROLLED -> out.put("reason", "none_enrolled")
            BiometricManager.BIOMETRIC_ERROR_NO_HARDWARE, BiometricManager.BIOMETRIC_ERROR_HW_UNAVAILABLE -> out.put("reason", "no_hardware")
            BiometricManager.BIOMETRIC_ERROR_SECURITY_UPDATE_REQUIRED -> out.put("reason", "security_update_required")
        }
        call.resolve(out)
    }

    @PluginMethod
    fun authenticate(call: PluginCall) {
        val act = activity ?: return call.reject("no activity", "unavailable")
        act.runOnUiThread {
            val prompt = BiometricPrompt(
                act,
                ContextCompat.getMainExecutor(act),
                object : BiometricPrompt.AuthenticationCallback() {
                    override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) = call.resolve()

                    override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                        val code = when (errorCode) {
                            BiometricPrompt.ERROR_USER_CANCELED, BiometricPrompt.ERROR_NEGATIVE_BUTTON, BiometricPrompt.ERROR_CANCELED -> "cancelled"
                            BiometricPrompt.ERROR_LOCKOUT, BiometricPrompt.ERROR_LOCKOUT_PERMANENT -> "lockout"
                            BiometricPrompt.ERROR_NO_BIOMETRICS, BiometricPrompt.ERROR_NO_DEVICE_CREDENTIAL -> "none_enrolled"
                            else -> "failed"
                        }
                        call.reject(errString.toString(), code)
                    }
                    // onAuthenticationFailed: a single non-matching attempt; the prompt stays open.
                },
            )
            val info = BiometricPrompt.PromptInfo.Builder()
                .setTitle(call.getString("title") ?: "TuxMail")
                .apply { call.getString("subtitle")?.let { setSubtitle(it) } }
                .setAllowedAuthenticators(authenticators)
                .setConfirmationRequired(false)
                .build()
            prompt.authenticate(info)
        }
    }

    // ---- share target ---------------------------------------------------------

    @PluginMethod
    fun getPendingShare(call: PluginCall) {
        call.resolve(ShareInbox.takePending() ?: JSObject())
    }

    // ---- push -------------------------------------------------------------------

    @PluginMethod
    fun pushInfo(call: PluginCall) {
        call.resolve(FlavorPush.info(context))
    }

    @PluginMethod
    fun pushRegister(call: PluginCall) {
        val act = activity ?: return call.reject("no activity", "unavailable")
        val vapid = call.getString("vapidKey")
        val distributor = call.getString("distributor")
        act.runOnUiThread {
            try {
                FlavorPush.register(act, vapid, distributor)
                call.resolve()
            } catch (e: Exception) {
                call.reject(e.message ?: "push registration failed", "register_failed", e)
            }
        }
    }

    @PluginMethod
    fun pushUnregister(call: PluginCall) {
        try {
            FlavorPush.unregister(context)
        } catch (_: Exception) {
            // best effort
        }
        call.resolve()
    }

    // ---- notifications ----------------------------------------------------------

    private fun notificationState(): String {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            val s = getPermissionState("notifications")
            if (s != PermissionState.GRANTED) return s.toString()
        }
        return if (NotificationManagerCompat.from(context).areNotificationsEnabled()) "granted" else "denied"
    }

    @PluginMethod
    override fun checkPermissions(call: PluginCall) {
        call.resolve(JSObject().put("notifications", notificationState()))
    }

    @PluginMethod
    override fun requestPermissions(call: PluginCall) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU && getPermissionState("notifications") != PermissionState.GRANTED) {
            requestPermissionForAlias("notifications", call, "onNotificationPermission")
        } else {
            checkPermissions(call)
        }
    }

    @PermissionCallback
    private fun onNotificationPermission(call: PluginCall) {
        checkPermissions(call)
    }

    @PluginMethod
    fun cancelConversationNotifications(call: PluginCall) {
        val id = call.getString("conversationId")
        if (!id.isNullOrEmpty()) PushRenderer.cancelConversation(context, id)
        call.resolve()
    }
}
