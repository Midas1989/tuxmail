// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"

	"github.com/Midas1989/Mailclient/internal/categorize"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/meeting"
	"github.com/Midas1989/Mailclient/internal/merge"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/spam"
	"github.com/Midas1989/Mailclient/internal/spell"
	tsync "github.com/Midas1989/Mailclient/internal/sync"
	"github.com/Midas1989/Mailclient/internal/unsubscribe"
)

// Mail comfort features (KONZEPT 8.6–8.11, 9.1): spam filter, newsletter
// unsubscription, smart categories, e-invoices, print/PDF, spell checking,
// mail merge, video conference links and S/MIME. The files
// comfort_*.go implement them on top of the feature packages.

// ComfortSettings are the user's settings of the comfort features.
type ComfortSettings struct {
	Spam       SpamSettings       `json:"spam"`
	Categories CategorySettings   `json:"categories"`
	Spell      SpellSettings      `json:"spell"`
	Meeting    MeetingSettings    `json:"meeting"`
	Merge      MergeSettings      `json:"merge"`
	SMIME      SMIMESettings      `json:"smime"`
	PGP        PGPSettings        `json:"pgp"`
	PDF        PDFSettings        `json:"pdf"`
	Newsletter NewsletterSettings `json:"newsletter"`
}

// SpamSettings configure the spam filter.
type SpamSettings struct {
	Enabled bool `json:"enabled"`
	// Thresholds 0..100: from Unsure on a mail is marked as suspicious,
	// from Spam on it counts as spam.
	Unsure int `json:"unsure"`
	Spam   int `json:"spam"`
	// MoveToJunk moves mails classified as spam into the junk folder.
	MoveToJunk bool `json:"moveToJunk"`
	// ServerLearn sets IMAP $Junk/$NotJunk keywords when training.
	ServerLearn bool `json:"serverLearn"`
}

// CategorySettings configure smart categories.
type CategorySettings struct {
	Enabled bool `json:"enabled"`
	// Tabs lists the categories shown as tabs (default all).
	Tabs []string `json:"tabs,omitempty"`
	// AIRefine lets the AI assistant refine uncertain results (local models only).
	AIRefine bool `json:"aiRefine"`
}

// SpellSettings configure the spell checker.
type SpellSettings struct {
	Enabled  bool   `json:"enabled"`
	Language string `json:"language"` // auto | de | en
}

// MeetingSettings configure video conference links.
type MeetingSettings struct {
	DefaultProvider string `json:"defaultProvider"` // jitsi | nextcloud_talk | teams
	JitsiBaseURL    string `json:"jitsiBaseUrl"`
	// NextcloudAccountID is the cloud account used for Talk (0 = first).
	NextcloudAccountID int64 `json:"nextcloudAccountId,omitempty"`
}

// MergeSettings configure mail merge.
type MergeSettings struct {
	PerMinute int `json:"perMinute"`
}

// SMIMESettings configure S/MIME defaults.
type SMIMESettings struct {
	// SignByDefault signs every mail of identities with a certificate.
	SignByDefault bool `json:"signByDefault"`
	// EncryptAutomatically encrypts when all recipients have certificates.
	EncryptAutomatically bool `json:"encryptAutomatically"`
	// Prefer is used when both OpenPGP and S/MIME are possible: "pgp" or "smime".
	Prefer string `json:"prefer"`
}

// PDFSettings configure PDF export.
type PDFSettings struct {
	// ChromiumPath overrides the browser used for HTML rendering.
	ChromiumPath string `json:"chromiumPath,omitempty"`
	// UseChromium renders HTML mails with Chromium when available.
	UseChromium bool `json:"useChromium"`
	// NameTemplate is the default file-name template.
	NameTemplate string `json:"nameTemplate"`
}

// NewsletterSettings configure the newsletter bar.
type NewsletterSettings struct {
	ShowBar bool `json:"showBar"`
}

// DefaultComfortSettings are used until the user saves settings.
func DefaultComfortSettings() ComfortSettings {
	return ComfortSettings{
		Spam:       SpamSettings{Enabled: true, Unsure: 50, Spam: 90, MoveToJunk: false, ServerLearn: true},
		Categories: CategorySettings{Enabled: true},
		Spell:      SpellSettings{Enabled: true, Language: "auto"},
		Meeting:    MeetingSettings{DefaultProvider: meeting.ProviderJitsi, JitsiBaseURL: "https://meet.jit.si"},
		Merge:      MergeSettings{PerMinute: 30},
		SMIME:      SMIMESettings{EncryptAutomatically: true, Prefer: "pgp"},
		PGP:        PGPSettings{SendAutocrypt: true},
		PDF:        PDFSettings{UseChromium: true, NameTemplate: "{{datum}}_{{absender}}_{{betreff}}.pdf"},
		Newsletter: NewsletterSettings{ShowBar: true},
	}
}

const comfortSettingsKey = "settings"

type comfortState struct {
	a *App

	mu       sync.Mutex
	settings *ComfortSettings

	spamC   *spam.Classifier
	catC    *categorize.Classifier
	spell   *spell.Registry
	unsub   *unsubscribe.Unsubscriber
	meeting *meeting.Service
	runner  *merge.Runner

	jobsMu sync.Mutex
	jobs   map[string]context.CancelFunc

	smimeMu    sync.Mutex
	smimeCache map[string]any // decoded identities by cert id

	pgp *pgpState // comfort_pgp.go
}

func newComfortState(a *App) *comfortState {
	c := &comfortState{a: a, jobs: map[string]context.CancelFunc{}, smimeCache: map[string]any{}}
	c.spamC = &spam.Classifier{Store: spamStore{a.st}}
	c.catC = &categorize.Classifier{Model: a.st}
	c.spell = spell.Default
	if a.opts.DataDir != "" {
		c.spell = &spell.Registry{Dir: filepath.Join(a.opts.DataDir, "dictionaries")}
	}
	c.unsub = &unsubscribe.Unsubscriber{Mail: comfortUnsubMail{a: a}}
	c.meeting = &meeting.Service{Talk: &meeting.TalkClient{Credentials: nextcloudLogins{a: a}, HTTP: a.opts.HTTPClient}}
	c.runner = &merge.Runner{Sender: mergeSender{a: a}}
	c.pgp = newPGPState(a)
	if r, ok := a.render.(*MIMERenderer); ok {
		r.Parse.SMIME = smimeReader{a: a}
		r.Parse.KeyRing = c.pgp.ring
		r.Parse.PGPSenderKeys = a.pgpSenderKeys
	}
	a.SetPDFRenderer(a.renderArchivePDF)
	return c
}

// ComfortSettings returns the settings.
func (a *App) ComfortSettings(ctx context.Context) (ComfortSettings, error) {
	c := a.comfort
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.settings != nil {
		return *c.settings, nil
	}
	s := DefaultComfortSettings()
	if v, err := a.st.ComfortGet(ctx, comfortSettingsKey); err != nil {
		return s, err
	} else if v != "" {
		_ = json.Unmarshal([]byte(v), &s)
	}
	c.settings = &s
	return s, nil
}

func (a *App) comfortSettings() ComfortSettings {
	s, _ := a.ComfortSettings(context.Background())
	return s
}

// SaveComfortSettings stores the settings.
func (a *App) SaveComfortSettings(ctx context.Context, s ComfortSettings) (ComfortSettings, error) {
	if s.Spam.Unsure <= 0 || s.Spam.Unsure > 100 {
		s.Spam.Unsure = 50
	}
	if s.Spam.Spam <= s.Spam.Unsure || s.Spam.Spam > 100 {
		s.Spam.Spam = max(90, s.Spam.Unsure+1)
		s.Spam.Spam = min(s.Spam.Spam, 100)
	}
	if s.Merge.PerMinute <= 0 {
		s.Merge.PerMinute = 30
	}
	if s.Spell.Language == "" {
		s.Spell.Language = "auto"
	}
	if s.SMIME.Prefer != "smime" {
		s.SMIME.Prefer = "pgp"
	}
	b, _ := json.Marshal(s)
	if err := a.st.ComfortSet(ctx, comfortSettingsKey, string(b)); err != nil {
		return s, err
	}
	a.comfort.mu.Lock()
	a.comfort.settings = &s
	a.comfort.mu.Unlock()
	a.chromiumBroken.Store(false) // retry the browser after a settings change
	return s, nil
}

// configureComfortSync installs the new-mail hook of the sync engine.
func (a *App) configureComfortSync(cfg *tsync.Config) {
	cfg.Comfort = comfortProcessor{a: a}
}

// configureComfortSend installs the S/MIME and OpenPGP keys into the send pipeline.
func (a *App) configureComfortSend(cfg *send.Config) {
	cfg.SMIME = smimeKeys{a: a}
	cfg.PGP = a.comfort.pgp.planner
	cfg.PGPPrefs = func() send.PGPPrefs {
		s := a.comfortSettings().PGP
		return send.PGPPrefs{Prefer: a.pgpOwnPrefer(), NoAutocrypt: !s.SendAutocrypt, SignAlways: s.SignByDefault}
	}
}

// comfortLoop runs the background work: spam moves and training on folder
// moves, and the insight backfill for mails whose body arrived later.
func (a *App) comfortLoop(ctx context.Context) {
	sub := a.bus.Subscribe(ctx, events.SubscribeOptions{Name: "comfort", Types: []events.Type{events.MailReceived, events.MailMoved}, Buffer: 256})
	tick := time.NewTicker(2 * time.Minute)
	defer tick.Stop()
	backfill := time.After(20 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			a.comfortEvent(ctx, ev)
		case <-backfill:
			a.backfillInsights(ctx, 200)
		case <-tick.C:
			a.backfillInsights(ctx, 200)
		}
	}
}
