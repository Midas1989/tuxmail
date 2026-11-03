// SPDX-License-Identifier: AGPL-3.0-or-later

package autoconfig

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// ClientConfig is the parsed Mozilla autoconfig document
// (https://wiki.mozilla.org/Thunderbird:Autoconfiguration:ConfigFileFormat).
type ClientConfig struct {
	XMLName            xml.Name       `xml:"clientConfig"`
	Version            string         `xml:"version,attr"`
	EmailProvider      EmailProvider  `xml:"emailProvider"`
	OAuth2             *OAuth2Config  `xml:"oAuth2"`
	AddressBooks       []DAVService   `xml:"addressBook"`
	Calendars          []DAVService   `xml:"calendar"`
	WebMail            *WebMailConfig `xml:"webMail"`
	ClientConfigUpdate *struct {
		URL string `xml:"url,attr"`
	} `xml:"clientConfigUpdate"`
}

// EmailProvider describes the mail servers of one provider.
type EmailProvider struct {
	ID               string          `xml:"id,attr"`
	Domains          []string        `xml:"domain"`
	DisplayName      string          `xml:"displayName"`
	DisplayShortName string          `xml:"displayShortName"`
	Incoming         []ServerEntry   `xml:"incomingServer"`
	Outgoing         []ServerEntry   `xml:"outgoingServer"`
	Documentation    []Documentation `xml:"documentation"`
	Enable           *EnableInfo     `xml:"enable"`
}

// ServerEntry is an <incomingServer> or <outgoingServer> element.
type ServerEntry struct {
	Type           string   `xml:"type,attr"` // imap, pop3, smtp, exchange
	Hostname       string   `xml:"hostname"`
	Port           int      `xml:"port"`
	SocketType     string   `xml:"socketType"`     // SSL, STARTTLS, plain
	Authentication []string `xml:"authentication"` // in order of preference
	Username       string   `xml:"username"`
	Password       string   `xml:"password"`
	OWAURL         string   `xml:"owaURL"`
	EWSURL         string   `xml:"ewsURL"`
	// POP3 specific
	POP3 *struct {
		LeaveMessagesOnServer bool `xml:"leaveMessagesOnServer"`
		DownloadOnBiff        bool `xml:"downloadOnBiff"`
		DaysToLeave           int  `xml:"daysToLeaveMessagesOnServer"`
		CheckInterval         *struct {
			Minutes int `xml:"minutes,attr"`
		} `xml:"checkInterval"`
	} `xml:"pop3"`
	// SMTP specific
	AddThisServer            *bool `xml:"addThisServer"`
	UseGlobalPreferredServer *bool `xml:"useGlobalPreferredServer"`
}

// Documentation links to provider help pages.
type Documentation struct {
	URL   string `xml:"url,attr"`
	Descr []struct {
		Lang string `xml:"lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"descr"`
}

// EnableInfo tells the user to enable IMAP/POP access in the web UI first.
type EnableInfo struct {
	VisitURL    string `xml:"visiturl,attr"`
	Instruction []struct {
		Lang string `xml:"lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"instruction"`
}

// OAuth2Config is the <oAuth2> element.
type OAuth2Config struct {
	Issuer   string `xml:"issuer"`
	Scope    string `xml:"scope"`
	AuthURL  string `xml:"authURL"`
	TokenURL string `xml:"tokenURL"`
}

// DAVService is an <addressBook> or <calendar> element.
type DAVService struct {
	Type           string `xml:"type,attr"` // carddav, caldav
	Username       string `xml:"username"`
	Authentication string `xml:"authentication"`
	ServerURL      string `xml:"serverURL"`
}

// WebMailConfig is the <webMail> element.
type WebMailConfig struct {
	LoginPage struct {
		URL string `xml:"url,attr"`
	} `xml:"loginPage"`
}

// maxXMLSize bounds autoconfig documents.
const maxXMLSize = 1 << 20

// ParseClientConfig parses a Mozilla autoconfig XML document and substitutes
// the placeholders %EMAILADDRESS%, %EMAILLOCALPART%, %EMAILDOMAIN% and
// %REALNAME% using email (and an empty real name).
func ParseClientConfig(r io.Reader, email string) (*ClientConfig, error) {
	var cfg ClientConfig
	dec := xml.NewDecoder(io.LimitReader(r, maxXMLSize))
	dec.Strict = false
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "utf-8", "us-ascii", "ascii", "iso-8859-1", "latin1":
			return input, nil
		}
		return nil, fmt.Errorf("autoconfig: unsupported charset %q", charset)
	}
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("autoconfig: parse config XML: %w", err)
	}
	if len(cfg.EmailProvider.Incoming) == 0 && len(cfg.EmailProvider.Outgoing) == 0 {
		return nil, errors.New("autoconfig: config XML lists no servers")
	}
	rep := placeholderReplacer(email)
	sub := func(s string) string { return strings.TrimSpace(rep.Replace(s)) }
	for _, list := range [][]ServerEntry{cfg.EmailProvider.Incoming, cfg.EmailProvider.Outgoing} {
		for i := range list {
			e := &list[i]
			e.Type = strings.ToLower(strings.TrimSpace(e.Type))
			e.Hostname = strings.ToLower(sub(e.Hostname))
			e.Username = sub(e.Username)
			e.SocketType = strings.TrimSpace(e.SocketType)
			e.OWAURL = sub(e.OWAURL)
			e.EWSURL = sub(e.EWSURL)
			for j := range e.Authentication {
				e.Authentication[j] = strings.TrimSpace(e.Authentication[j])
			}
		}
	}
	for _, list := range [][]DAVService{cfg.AddressBooks, cfg.Calendars} {
		for i := range list {
			list[i].Username = sub(list[i].Username)
			list[i].ServerURL = sub(list[i].ServerURL)
		}
	}
	for i := range cfg.EmailProvider.Domains {
		cfg.EmailProvider.Domains[i] = strings.ToLower(strings.TrimSpace(cfg.EmailProvider.Domains[i]))
	}
	cfg.EmailProvider.DisplayName = sub(cfg.EmailProvider.DisplayName)
	if cfg.OAuth2 != nil {
		cfg.OAuth2.Issuer = strings.TrimSpace(cfg.OAuth2.Issuer)
		cfg.OAuth2.Scope = strings.TrimSpace(cfg.OAuth2.Scope)
		cfg.OAuth2.AuthURL = strings.TrimSpace(cfg.OAuth2.AuthURL)
		cfg.OAuth2.TokenURL = strings.TrimSpace(cfg.OAuth2.TokenURL)
	}
	return &cfg, nil
}

func placeholderReplacer(email string) *strings.Replacer {
	local, dom, _ := strings.Cut(email, "@")
	return strings.NewReplacer(
		"%EMAILADDRESS%", email,
		"%EMAILLOCALPART%", local,
		"%EMAILDOMAIN%", dom,
		"%REALNAME%", "",
	)
}

// Candidates converts the document into ranked candidates. Every usable
// incoming server (IMAP before POP3, document order otherwise) is paired with
// the first usable SMTP server.
func (cfg *ClientConfig) Candidates(src Source) []Candidate {
	var out *ServerEntry
	var outCfg domain.ServerConfig
	for i := range cfg.EmailProvider.Outgoing {
		e := &cfg.EmailProvider.Outgoing[i]
		if e.Type != "smtp" {
			continue
		}
		if sc, ok := e.serverConfig(); ok {
			out, outCfg = e, sc
			break
		}
	}

	var oauthProvider string
	if cfg.OAuth2 != nil {
		oauthProvider = OAuthProviderForIssuer(cfg.OAuth2.Issuer)
	}
	var calURL, cardURL string
	for _, c := range cfg.Calendars {
		if strings.EqualFold(c.Type, "caldav") && c.ServerURL != "" {
			calURL = c.ServerURL
			break
		}
	}
	for _, c := range cfg.AddressBooks {
		if strings.EqualFold(c.Type, "carddav") && c.ServerURL != "" {
			cardURL = c.ServerURL
			break
		}
	}

	var cands []Candidate
	for _, kind := range []string{"imap", "pop3"} {
		for i := range cfg.EmailProvider.Incoming {
			e := &cfg.EmailProvider.Incoming[i]
			if e.Type != kind {
				continue
			}
			in, ok := e.serverConfig()
			if !ok {
				continue
			}
			c := Candidate{
				Kind:        domain.AccountKind(kind),
				Incoming:    in,
				Source:      src,
				DisplayName: cfg.EmailProvider.DisplayName,
				CalDAVURL:   calURL,
				CardDAVURL:  cardURL,
			}
			if out != nil {
				c.Outgoing = outCfg
			}
			if in.Auth == domain.AuthOAuth2 || (out != nil && outCfg.Auth == domain.AuthOAuth2) {
				c.OAuthProvider = oauthProvider
			}
			cands = append(cands, c)
		}
	}
	return cands
}

func (e *ServerEntry) serverConfig() (domain.ServerConfig, bool) {
	if e.Hostname == "" {
		return domain.ServerConfig{}, false
	}
	var tlsMode domain.TLSMode
	switch strings.ToUpper(e.SocketType) {
	case "SSL", "TLS":
		tlsMode = domain.TLSImplicit
	case "STARTTLS":
		tlsMode = domain.TLSStartTLS
	case "PLAIN", "":
		tlsMode = domain.TLSNone
	default:
		return domain.ServerConfig{}, false
	}
	auth, ok := pickAuth(e.Authentication)
	if !ok {
		return domain.ServerConfig{}, false
	}
	port := e.Port
	if port == 0 {
		port = defaultPort(e.Type, tlsMode)
	}
	if port == 0 {
		return domain.ServerConfig{}, false
	}
	return domain.ServerConfig{
		Host: e.Hostname, Port: port, TLS: tlsMode, Auth: auth, Username: e.Username,
	}, true
}

// pickAuth returns the first supported authentication method in document
// order (documents list the preferred method first).
func pickAuth(methods []string) (domain.AuthMethod, bool) {
	if len(methods) == 0 {
		return domain.AuthPassword, true
	}
	for _, m := range methods {
		switch strings.ToLower(m) {
		case "oauth2":
			return domain.AuthOAuth2, true
		case "password-cleartext", "plain", "password-encrypted", "secure":
			return domain.AuthPassword, true
		case "tls-client-cert":
			return domain.AuthClientCert, true
		case "none", "client-ip-address", "smtp-after-pop":
			return domain.AuthPassword, true
		}
	}
	return "", false
}

func defaultPort(kind string, mode domain.TLSMode) int {
	switch kind {
	case "imap":
		if mode == domain.TLSImplicit {
			return 993
		}
		return 143
	case "pop3":
		if mode == domain.TLSImplicit {
			return 995
		}
		return 110
	case "smtp":
		if mode == domain.TLSImplicit {
			return 465
		}
		return 587
	}
	return 0
}

// OAuthProviderForIssuer maps an OAuth2 issuer host to TuxMail's provider key.
func OAuthProviderForIssuer(issuer string) string {
	issuer = strings.ToLower(strings.TrimSpace(issuer))
	issuer = strings.TrimPrefix(strings.TrimPrefix(issuer, "https://"), "http://")
	issuer = strings.TrimSuffix(issuer, "/")
	switch {
	case issuer == "":
		return ""
	case issuer == "accounts.google.com":
		return OAuthGoogle
	case strings.HasPrefix(issuer, "login.microsoftonline.com"), strings.HasPrefix(issuer, "login.live.com"):
		return OAuthMicrosoft
	case strings.HasSuffix(issuer, "login.yahoo.com"):
		return OAuthYahoo
	case strings.HasSuffix(issuer, "aol.com"):
		return "aol"
	}
	return issuer
}

// itoa is a tiny helper used for URL building.
func itoa(i int) string { return strconv.Itoa(i) }
