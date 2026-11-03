// SPDX-License-Identifier: AGPL-3.0-or-later

package compose

import (
	"strings"
	"time"
)

// Locale texts for generated mail content. This small table is temporary:
// it will be replaced by the frontend's locales/*.json once the backend i18n
// loader exists; keys follow the same naming ("compose.<key>").
type texts struct {
	// Attribution is the reply attribution with {date} and {from}.
	Attribution string
	// DateLayout formats {date} in attributions and forward headers.
	DateLayout string
	// ForwardMarker starts an inline-forwarded block.
	ForwardMarker               string
	From, Date, Subject, To, Cc string
	// CloudLink* build the cloud link block (KONZEPT 12.2).
	CloudLinkIntro, CloudLinkLink, CloudLinkPassword, CloudLinkValidUntil, CloudLinkPasswordSeparate string
	// PasswordMailSubject / PasswordMailBody are used for the separate
	// password mail; {subject} and {list} are replaced.
	PasswordMailSubject, PasswordMailBody string
	// ReplyPrefix / ForwardPrefix are the localized subject prefixes
	// (only used when Draft.LocalizedPrefixes is set).
	ReplyPrefix, ForwardPrefix string
}

var localeTexts = map[string]texts{
	"en": {
		Attribution:   "On {date}, {from} wrote:",
		DateLayout:    "Mon, Jan 2, 2006 at 3:04 PM",
		ForwardMarker: "---------- Forwarded message ---------",
		From:          "From", Date: "Date", Subject: "Subject", To: "To", Cc: "Cc",
		CloudLinkIntro:            "Files shared via link:",
		CloudLinkLink:             "Link",
		CloudLinkPassword:         "Password",
		CloudLinkValidUntil:       "valid until",
		CloudLinkPasswordSeparate: "The password will be sent separately.",
		PasswordMailSubject:       "Password for: {subject}",
		PasswordMailBody:          "Here are the passwords for the files shared in the message \"{subject}\":\n\n{list}",
		ReplyPrefix:               "Re:",
		ForwardPrefix:             "Fwd:",
	},
	"de": {
		Attribution:   "Am {date} schrieb {from}:",
		DateLayout:    "02.01.2006 um 15:04",
		ForwardMarker: "-------- Weitergeleitete Nachricht --------",
		From:          "Von", Date: "Datum", Subject: "Betreff", To: "An", Cc: "Kopie",
		CloudLinkIntro:            "Per Link freigegebene Dateien:",
		CloudLinkLink:             "Link",
		CloudLinkPassword:         "Passwort",
		CloudLinkValidUntil:       "gültig bis",
		CloudLinkPasswordSeparate: "Das Passwort erhalten Sie separat.",
		PasswordMailSubject:       "Passwort zu: {subject}",
		PasswordMailBody:          "Hier die Passwörter zu den Dateien aus der Nachricht „{subject}“:\n\n{list}",
		ReplyPrefix:               "AW:",
		ForwardPrefix:             "WG:",
	},
}

// textsFor returns the texts for a locale ("de", "de-DE", "en_US" ...),
// falling back to English.
func textsFor(locale string) texts {
	l := strings.ToLower(locale)
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	if t, ok := localeTexts[l]; ok {
		return t
	}
	return localeTexts["en"]
}

// formatDate formats t for display in the given locale and zone.
func formatDate(t time.Time, locale string, loc *time.Location) string {
	if t.IsZero() {
		return ""
	}
	if loc != nil {
		t = t.In(loc)
	}
	return t.Format(textsFor(locale).DateLayout)
}

// CloudLinkLabels exposes the localized labels for the cloud link block to
// the send pipeline.
type CloudLinkLabels struct {
	Intro, Link, Password, ValidUntil, PasswordSeparate string
	PasswordMailSubject, PasswordMailBody               string
	DateLayout                                          string
}

// CloudLinkTexts returns the cloud link labels for locale.
func CloudLinkTexts(locale string) CloudLinkLabels {
	t := textsFor(locale)
	layout := "Jan 2, 2006"
	if strings.HasPrefix(strings.ToLower(locale), "de") {
		layout = "02.01.2006"
	}
	return CloudLinkLabels{
		Intro: t.CloudLinkIntro, Link: t.CloudLinkLink, Password: t.CloudLinkPassword,
		ValidUntil: t.CloudLinkValidUntil, PasswordSeparate: t.CloudLinkPasswordSeparate,
		PasswordMailSubject: t.PasswordMailSubject, PasswordMailBody: t.PasswordMailBody,
		DateLayout: layout,
	}
}
