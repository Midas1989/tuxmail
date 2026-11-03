// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// UsernameStyle describes how the login name is derived from the address.
type UsernameStyle int

const (
	UsernameEmail     UsernameStyle = iota // full address
	UsernameLocalPart                      // part before '@'
)

// Provider is an entry of the built-in provider table.
type Provider struct {
	ID          string
	DisplayName string
	// Domains are the mail domains hosted by the provider.
	Domains []string
	// MXSuffixes match MX host names of custom domains hosted by the provider.
	MXSuffixes []string
	Incoming   domain.ServerConfig // Username is filled in from Username
	Outgoing   domain.ServerConfig
	Username   UsernameStyle
	// OAuthProvider is set when the provider expects OAuth2.
	OAuthProvider string
	// CalDAVURL / CardDAVURL may contain %EMAILADDRESS% placeholders.
	CalDAVURL  string
	CardDAVURL string
}

// Candidate builds the candidate for email.
func (p *Provider) Candidate(email string, src Source) Candidate {
	user := email
	if p.Username == UsernameLocalPart {
		user, _, _ = strings.Cut(email, "@")
	}
	in, out := p.Incoming, p.Outgoing
	in.Username, out.Username = user, user
	rep := placeholderReplacer(email)
	return Candidate{
		Kind:          domain.KindIMAP,
		Incoming:      in,
		Outgoing:      out,
		Source:        src,
		OAuthProvider: p.OAuthProvider,
		CalDAVURL:     rep.Replace(p.CalDAVURL),
		CardDAVURL:    rep.Replace(p.CardDAVURL),
		DisplayName:   p.DisplayName,
	}
}

func imapTLS(host string, auth domain.AuthMethod) domain.ServerConfig {
	return domain.ServerConfig{Host: host, Port: 993, TLS: domain.TLSImplicit, Auth: auth}
}

func smtpTLS(host string, auth domain.AuthMethod) domain.ServerConfig {
	return domain.ServerConfig{Host: host, Port: 465, TLS: domain.TLSImplicit, Auth: auth}
}

func smtpStartTLS(host string, auth domain.AuthMethod) domain.ServerConfig {
	return domain.ServerConfig{Host: host, Port: 587, TLS: domain.TLSStartTLS, Auth: auth}
}

// KnownProviders returns a fresh copy of the built-in provider table.
func KnownProviders() []Provider {
	pw, oa := domain.AuthPassword, domain.AuthOAuth2
	return []Provider{
		{
			ID: "gmail", DisplayName: "Google Mail",
			Domains:       []string{"gmail.com", "googlemail.com"},
			MXSuffixes:    []string{"google.com", "googlemail.com"},
			Incoming:      imapTLS("imap.gmail.com", oa),
			Outgoing:      smtpTLS("smtp.gmail.com", oa),
			OAuthProvider: OAuthGoogle,
			CalDAVURL:     "https://apidata.googleusercontent.com/caldav/v2/%EMAILADDRESS%/user",
			CardDAVURL:    "https://www.googleapis.com/carddav/v1/principals/%EMAILADDRESS%/lists/default/",
		},
		{
			ID: "outlook", DisplayName: "Microsoft Outlook.com / Microsoft 365",
			Domains: []string{
				"outlook.com", "outlook.de", "hotmail.com", "hotmail.de", "hotmail.co.uk",
				"live.com", "live.de", "msn.com", "passport.com",
			},
			MXSuffixes:    []string{"mail.protection.outlook.com", "olc.protection.outlook.com", "outlook.com"},
			Incoming:      imapTLS("outlook.office365.com", oa),
			Outgoing:      smtpStartTLS("smtp.office365.com", oa),
			OAuthProvider: OAuthMicrosoft,
		},
		{
			ID: "icloud", DisplayName: "iCloud Mail",
			Domains:    []string{"icloud.com", "me.com", "mac.com"},
			MXSuffixes: []string{"mail.icloud.com"},
			Incoming:   imapTLS("imap.mail.me.com", pw),
			Outgoing:   smtpStartTLS("smtp.mail.me.com", pw),
			CalDAVURL:  "https://caldav.icloud.com/",
			CardDAVURL: "https://contacts.icloud.com/",
		},
		{
			ID: "gmx", DisplayName: "GMX",
			Domains:    []string{"gmx.de", "gmx.net", "gmx.at", "gmx.ch", "gmx.eu", "gmx.info", "gmx.biz", "gmx.org"},
			MXSuffixes: []string{"gmx.net"},
			Incoming:   imapTLS("imap.gmx.net", pw),
			Outgoing:   smtpTLS("mail.gmx.net", pw),
			CalDAVURL:  "https://caldav.gmx.net/",
			CardDAVURL: "https://carddav.gmx.net/",
		},
		{
			ID: "gmx.com", DisplayName: "GMX",
			Domains:    []string{"gmx.com", "gmx.us", "gmx.co.uk", "gmx.fr", "gmx.es"},
			MXSuffixes: []string{"gmx.com"},
			Incoming:   imapTLS("imap.gmx.com", pw),
			Outgoing:   smtpTLS("mail.gmx.com", pw),
		},
		{
			ID: "web.de", DisplayName: "WEB.DE",
			Domains:    []string{"web.de"},
			MXSuffixes: []string{"web.de"},
			Incoming:   imapTLS("imap.web.de", pw),
			Outgoing:   smtpStartTLS("smtp.web.de", pw),
			Username:   UsernameLocalPart,
			CalDAVURL:  "https://caldav.web.de/",
			CardDAVURL: "https://carddav.web.de/",
		},
		{
			ID: "t-online", DisplayName: "Telekom Mail (t-online.de)",
			Domains:    []string{"t-online.de", "magenta.de"},
			MXSuffixes: []string{"t-online.de"},
			Incoming:   imapTLS("secureimap.t-online.de", pw),
			Outgoing:   smtpTLS("securesmtp.t-online.de", pw),
		},
		{
			ID: "posteo", DisplayName: "Posteo",
			Domains: []string{
				"posteo.de", "posteo.net", "posteo.at", "posteo.ch", "posteo.eu", "posteo.org",
				"posteo.be", "posteo.us", "posteo.uk", "posteo.nl", "posteo.dk", "posteo.es",
			},
			MXSuffixes: []string{"posteo.de"},
			Incoming:   imapTLS("posteo.de", pw),
			Outgoing:   smtpStartTLS("posteo.de", pw),
			CalDAVURL:  "https://posteo.de:8443/",
			CardDAVURL: "https://posteo.de:8843/",
		},
		{
			ID: "mailbox.org", DisplayName: "mailbox.org",
			Domains:    []string{"mailbox.org"},
			MXSuffixes: []string{"mailbox.org"},
			Incoming:   imapTLS("imap.mailbox.org", pw),
			Outgoing:   smtpTLS("smtp.mailbox.org", pw),
			CalDAVURL:  "https://dav.mailbox.org/caldav/",
			CardDAVURL: "https://dav.mailbox.org/carddav/",
		},
		{
			ID: "ionos", DisplayName: "IONOS",
			Domains:    []string{"ionos.de", "ionos.com", "online.de", "onlinehome.de"},
			MXSuffixes: []string{"ionos.de", "kundenserver.de", "ionos.com", "perfora.net"},
			Incoming:   imapTLS("imap.ionos.de", pw),
			Outgoing:   smtpTLS("smtp.ionos.de", pw),
		},
		{
			ID: "strato", DisplayName: "STRATO",
			Domains:    []string{"strato.de"},
			MXSuffixes: []string{"rzone.de", "strato.de"},
			Incoming:   imapTLS("imap.strato.de", pw),
			Outgoing:   smtpTLS("smtp.strato.de", pw),
		},
		{
			ID: "yahoo", DisplayName: "Yahoo Mail",
			Domains: []string{
				"yahoo.com", "yahoo.de", "yahoo.co.uk", "yahoo.fr", "yahoo.es", "yahoo.it",
				"ymail.com", "rocketmail.com",
			},
			MXSuffixes: []string{"yahoodns.net"},
			Incoming:   imapTLS("imap.mail.yahoo.com", pw),
			Outgoing:   smtpTLS("smtp.mail.yahoo.com", pw),
		},
	}
}

func (d *Discoverer) known() []Provider {
	if d.Known != nil {
		return d.Known
	}
	return KnownProviders()
}

func (d *Discoverer) knownByDomain(dom string) *Provider {
	known := d.known()
	for i := range known {
		for _, kd := range known[i].Domains {
			if dom == kd {
				return &known[i]
			}
		}
	}
	return nil
}

func (d *Discoverer) knownByMX(mxHost string) *Provider {
	mxHost = strings.ToLower(strings.TrimSuffix(mxHost, "."))
	known := d.known()
	for i := range known {
		for _, s := range known[i].MXSuffixes {
			if mxHost == s || strings.HasSuffix(mxHost, "."+s) {
				return &known[i]
			}
		}
	}
	return nil
}
