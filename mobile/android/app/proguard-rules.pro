# SPDX-License-Identifier: AGPL-3.0-or-later
#
# R8 rules of the release build (minifyEnabled/shrinkResources in build.gradle,
# re-audit ND-15). Conservative on purpose: Capacitor finds plugins and their
# methods by reflection and annotations, so its whole runtime and the app's
# own plugin stay un-obfuscated; everything else (androidx, Firebase,
# UnifiedPush) ships its own consumer rules. NOT built or run by the audit
# tooling - the first release build has to be smoke-tested on a device
# (push registration, notification actions, share target, biometric lock).

# Stack traces: keep file names and line numbers (the mapping file of the
# build deobfuscates them).
-keepattributes SourceFile,LineNumberTable,*Annotation*,Signature,InnerClasses,EnclosingMethod
-renamesourcefileattribute SourceFile

# Capacitor: plugins are instantiated and invoked reflectively.
-keep class com.getcapacitor.** { *; }
-keep public class * extends com.getcapacitor.Plugin
-keep @com.getcapacitor.annotation.CapacitorPlugin public class * {
    @com.getcapacitor.annotation.PermissionCallback <methods>;
    @com.getcapacitor.annotation.ActivityCallback <methods>;
    @com.getcapacitor.PluginMethod public <methods>;
}
-keepclassmembers class * {
    @com.getcapacitor.PluginMethod public <methods>;
}
# Cordova compatibility layer shipped with Capacitor.
-keep class org.apache.cordova.** { *; }
-dontwarn org.apache.cordova.**

# JavaScript bridges of any WebView.
-keepclassmembers class * {
    @android.webkit.JavascriptInterface <methods>;
}

# The app: Kotlin objects reached from the manifest or from Capacitor.
-keep class org.tuxmail.app.** { *; }

# Kotlin metadata for reflection-based libraries.
-keep class kotlin.Metadata { *; }
-dontwarn kotlin.**
