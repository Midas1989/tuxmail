// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// Native texts (message boxes before the window exists, silent mode).
// The window itself uses the dictionary in ui/i18n.js.
var messages = map[string]map[string]string{
	"en": {
		"title":            "TuxMail Setup",
		"wv2Missing":       "TuxMail and this setup need the Microsoft Edge WebView2 Runtime, which is not installed.\n\nInstall it now? (about 2 MB download, then the runtime is installed by Microsoft's installer)",
		"wv2Failed":        "The WebView2 Runtime could not be installed:\n%v\n\nOpen the download page? After installing the runtime, start this setup again.\n\nAlternatives: install silently with \"/S\", or use the portable ZIP.",
		"oldWindows":       "TuxMail requires Windows 10 or later.",
		"usage":            "Invalid command line: %v\n\n%s",
		"failed":           "Setup failed:\n%v",
		"noPayload":        "This setup program contains no application files (development build).",
		"elevateRefused":   "Administrator rights are required for an installation for all users.",
		"uninstallConfirm": "Remove TuxMail from this PC?\n\nYour accounts, mail cache and settings (%APPDATA%\\TuxMail) are kept.",
	},
	"de": {
		"title":            "TuxMail Setup",
		"wv2Missing":       "TuxMail und dieses Setup benötigen die Microsoft Edge WebView2 Runtime, die nicht installiert ist.\n\nJetzt installieren? (etwa 2 MB Download, danach installiert Microsofts Installationsprogramm die Runtime)",
		"wv2Failed":        "Die WebView2 Runtime konnte nicht installiert werden:\n%v\n\nDownload-Seite öffnen? Nach der Installation der Runtime dieses Setup erneut starten.\n\nAlternativen: stille Installation mit „/S“ oder das portable ZIP.",
		"oldWindows":       "TuxMail benötigt Windows 10 oder neuer.",
		"usage":            "Ungültige Kommandozeile: %v\n\n%s",
		"failed":           "Setup fehlgeschlagen:\n%v",
		"noPayload":        "Dieses Setup enthält keine Programmdateien (Entwicklungs-Build).",
		"elevateRefused":   "Für eine Installation für alle Benutzer sind Administratorrechte erforderlich.",
		"uninstallConfirm": "TuxMail von diesem PC entfernen?\n\nIhre Konten, der E-Mail-Cache und die Einstellungen (%APPDATA%\\TuxMail) bleiben erhalten.",
	},
}

func msg(lang, key string) string {
	if m, ok := messages[lang][key]; ok {
		return m
	}
	return messages["en"][key]
}
