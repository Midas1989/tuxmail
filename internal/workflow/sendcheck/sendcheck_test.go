// SPDX-License-Identifier: AGPL-3.0-or-later

package sendcheck

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
)

func TestValidIBAN(t *testing.T) {
	valid := []string{"DE89 3704 0044 0532 0130 00", "DE89370400440532013000", "GB82 WEST 1234 5698 7654 32",
		"CH93 0076 2011 6238 5295 7", "AT61 1904 3002 3457 3201", "de89370400440532013000"}
	for _, s := range valid {
		if !ValidIBAN(s) {
			t.Errorf("%s should be valid", s)
		}
	}
	invalid := []string{"DE89 3704 0044 0532 0130 01", "DE8937040044053201300", "XX00", "DE89 3704 0044 0532 0130 0!",
		"1289370400440532013000", ""}
	for _, s := range invalid {
		if ValidIBAN(s) {
			t.Errorf("%s should be invalid", s)
		}
	}
	text := "Bitte überweisen an DE89 3704 0044 0532 0130 00 BIC COBADEFFXXX, nicht an DE89 3704 0044 0532 0130 01."
	if got := FindIBANs(text); !reflect.DeepEqual(got, []string{"DE89370400440532013000"}) {
		t.Fatalf("FindIBANs = %v", got)
	}
}

func TestLuhnAndCards(t *testing.T) {
	for _, s := range []string{"4111111111111111", "378282246310005", "5555555555554444", "79927398713"} {
		if !LuhnValid(s) {
			t.Errorf("%s should pass Luhn", s)
		}
	}
	for _, s := range []string{"4111111111111112", "79927398710", "1", "41111a1111111111"} {
		if LuhnValid(s) {
			t.Errorf("%s should fail Luhn", s)
		}
	}
	text := "Karte: 4111 1111 1111 1111, Amex 3782-822463-10005, Bestellnr 1234567890123 (no card), " +
		"falsch 4111 1111 1111 1112, IBAN DE89 3704 0044 0532 0130 00"
	got := FindCardNumbers(text)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"378282246310005", "4111111111111111"}) {
		t.Fatalf("cards = %v", got)
	}
	if pw := FindPasswords("Hallo,\nPasswort: geheim123\n"); len(pw) != 1 {
		t.Fatalf("passwords = %v", pw)
	}
	if pw := FindPasswords("Ich habe mein Passwort vergessen."); len(pw) != 0 {
		t.Fatalf("false positive %v", pw)
	}
}

type keys map[string]bool

func (k keys) HasKey(e string) bool { return k[e] }

type contacts map[string]bool

func (c contacts) IsBusinessContact(e string) (bool, bool) { b, ok := c[e]; return b, ok }

func addrs(s ...string) []domain.Address {
	out := make([]domain.Address, len(s))
	for i, e := range s {
		out[i] = domain.Address{Email: e}
	}
	return out
}

var now = time.Date(2026, 11, 3, 14, 0, 0, 0, time.UTC) // Tuesday

func cfg() Config {
	return Config{OwnDomains: []string{"firma.de"}, WorkingHours: WorkingHours{Location: time.UTC}}
}

func checks(fs []Finding) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range fs {
		m[f.Check] = f
	}
	return m
}

func goodDraft() *events.Draft {
	return &events.Draft{
		Identity: events.Identity{Email: "me@firma.de"}, To: addrs("kollege@firma.de"),
		Subject: "Projekt", Body: "Hallo, kurzer Stand.\n\nGruß",
	}
}

func TestCleanDraftHasNoFindings(t *testing.T) {
	if fs := Run(goodDraft(), cfg(), Deps{}, now); len(fs) != 0 {
		t.Fatalf("findings: %+v", fs)
	}
}

func TestChecks(t *testing.T) {
	type tc struct {
		name  string
		mod   func(d *events.Draft)
		deps  Deps
		check CheckID
		sev   Severity
	}
	cases := []tc{
		{"no recipients", func(d *events.Draft) { d.To = nil }, Deps{}, NoRecipients, Block},
		{"missing subject", func(d *events.Draft) { d.Subject = "  " }, Deps{}, MissingSubject, Warn},
		{"empty body", func(d *events.Draft) { d.Body = "\n> quoted only\n" }, Deps{}, EmptyBody, Warn},
		{"placeholder", func(d *events.Draft) { d.Body = "Hallo {{vorname}}," }, Deps{}, UnreplacedPlaceholder, Warn},
		{"forgotten attachment de", func(d *events.Draft) { d.Body = "Anbei die Unterlagen." }, Deps{}, ForgottenAttachment, Warn},
		{"forgotten attachment en", func(d *events.Draft) { d.Body = "Please see the attached file." }, Deps{}, ForgottenAttachment, Warn},
		{"large attachment", func(d *events.Draft) {
			d.Attachments = []events.Attachment{{Name: "video.mp4", Size: 30 << 20}}
		}, Deps{}, AttachmentTooLarge, Info},
		{"external", func(d *events.Draft) { d.Cc = addrs("kunde@kunde.de") }, Deps{}, ExternalRecipients, Info},
		{"many external", func(d *events.Draft) {
			d.To = addrs("a@x.de", "b@y.de", "c@z.de", "d@w.de", "e@v.de")
		}, Deps{}, ManyExternal, Warn},
		{"private address", func(d *events.Draft) { d.Cc = addrs("max@gmx.de") }, Deps{}, PrivateAddress, Warn},
		{"wrong identity", func(d *events.Draft) {
			d.Identity.Email = "max@web.de"
			d.To = addrs("partner@kunde.de")
		}, Deps{Contacts: contacts{"partner@kunde.de": true}}, WrongIdentity, Warn},
		{"pgp key", func(d *events.Draft) {}, Deps{Keys: keys{"kollege@firma.de": true}}, PGPKeyAvailable, Info},
		{"iban", func(d *events.Draft) { d.Body = "Konto: DE89 3704 0044 0532 0130 00" }, Deps{}, SensitiveData, Warn},
		{"card", func(d *events.Draft) { d.Body = "Nummer 4111-1111-1111-1111" }, Deps{}, SensitiveData, Warn},
		{"password", func(d *events.Draft) { d.Body = "Zugang:\nPasswort: Sommer2026!" }, Deps{}, SensitiveData, Warn},
		{"confidential", func(d *events.Draft) { d.Subject = "Vertraulich: Gehalt 2027" }, Deps{}, ConfidentialUnencrypted, Info},
		{"reply all", func(d *events.Draft) {
			d.ReplyAll = true
			d.To = addrs("1@firma.de", "2@firma.de", "3@firma.de", "4@firma.de", "5@firma.de")
			d.Cc = addrs("6@firma.de", "7@firma.de", "8@firma.de", "9@firma.de", "10@firma.de")
		}, Deps{}, ReplyAllLarge, Warn},
		{"old conversation", func(d *events.Draft) { d.InReplyToDate = now.AddDate(-1, 0, 0) }, Deps{}, OldConversation, Info},
		{"working hours", func(d *events.Draft) { d.SendAt = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC) }, Deps{}, OutsideWorkingHours, Info},
	}
	for _, c := range cases {
		d := goodDraft()
		c.mod(d)
		fs := Run(d, cfg(), c.deps, now)
		f, ok := checks(fs)[string(c.check)]
		if !ok {
			t.Errorf("%s: %s not reported (got %+v)", c.name, c.check, fs)
			continue
		}
		if f.Severity != c.sev || f.Key != "sendcheck."+string(c.check) || f.DE == "" || f.EN == "" {
			t.Errorf("%s: finding %+v", c.name, f)
		}
	}
}

func TestChecksNegative(t *testing.T) {
	// Attachment keyword only in the quoted part.
	d := goodDraft()
	d.Body = "Danke!\n\nAm 01.11.2026 schrieb Anna:\n> anbei die Datei\n"
	if _, ok := checks(Run(d, cfg(), Deps{}, now))[string(ForgottenAttachment)]; ok {
		t.Error("quoted keyword triggered forgotten attachment")
	}
	// "attachment" inside a longer word does not count; attached files silence it.
	d = goodDraft()
	d.Body = "anbei"
	d.Attachments = []events.Attachment{{Name: "a.pdf", Size: 10}}
	if _, ok := checks(Run(d, cfg(), Deps{}, now))[string(ForgottenAttachment)]; ok {
		t.Error("attachment present but warned")
	}
	// Encryption silences sensitive-data and pgp checks.
	d = goodDraft()
	d.Body = "IBAN DE89 3704 0044 0532 0130 00, vertraulich"
	d.Encrypt = true
	fs := checks(Run(d, cfg(), Deps{Keys: keys{"kollege@firma.de": true}}, now))
	for _, id := range []CheckID{SensitiveData, ConfidentialUnencrypted, PGPKeyAvailable} {
		if _, ok := fs[string(id)]; ok {
			t.Errorf("%s reported for encrypted mail", id)
		}
	}
	// Private identity writing privately: nothing to complain.
	d = goodDraft()
	d.Identity.Email = "max@gmx.de"
	d.To = addrs("oma@web.de")
	if fs := Run(d, cfg(), Deps{Contacts: contacts{"oma@web.de": false}}, now); len(fs) != 0 {
		t.Errorf("private mail findings %+v", fs)
	}
	// Recipient in another time zone still within working hours.
	d = goodDraft()
	d.SendAt = time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC) // 20:00 UTC = 12:00 in LA
	la, _ := time.LoadLocation("America/Los_Angeles")
	deps := Deps{}
	if la != nil {
		deps.RecipientLocation = func(string) *time.Location { return la }
		if _, ok := checks(Run(d, cfg(), deps, now))[string(OutsideWorkingHours)]; ok {
			t.Error("recipient time zone ignored")
		}
	}
}

func TestConfigOverrides(t *testing.T) {
	d := goodDraft()
	d.Subject = ""
	c := cfg()
	c.Severity = map[CheckID]Severity{MissingSubject: Block}
	fs := Run(d, c, Deps{}, now)
	if len(fs) != 1 || fs[0].Severity != Block {
		t.Fatalf("override: %+v", fs)
	}
	c.Disabled = map[CheckID]bool{MissingSubject: true}
	if fs := Run(d, c, Deps{}, now); len(fs) != 0 {
		t.Fatalf("disabled: %+v", fs)
	}
	if dc := DefaultConfig(); dc.ManyExternalMin != 5 || len(dc.FreemailDomains) == 0 {
		t.Fatal("defaults")
	}
	for _, want := range []string{"gmail.com", "gmx.de", "web.de", "t-online.de", "outlook.com", "hotmail.com", "yahoo.com", "icloud.com", "posteo.de", "mailbox.org", "proton.me"} {
		found := false
		for _, d := range DefaultFreemailDomains {
			found = found || d == want
		}
		if !found {
			t.Errorf("freemail list misses %s", want)
		}
	}
}

func TestHookAndOrdering(t *testing.T) {
	b := events.New()
	b.RegisterHook(events.MailBeforeSend, "sendcheck", Hook(cfg, Deps{}, func() time.Time { return now }))
	d := goodDraft()
	d.To = nil
	d.Subject = ""
	d.Body = "Hallo {{name}}"
	res := b.RunHooks(context.Background(), events.Event{Type: events.MailBeforeSend, Draft: d})
	if !res.Vetoed || len(res.Findings) != 3 || res.Findings[0].Check != string(NoRecipients) {
		t.Fatalf("res %+v", res)
	}
	if !strings.Contains(res.Findings[2].EN, "{{name}}") {
		t.Fatalf("placeholder text %+v", res.Findings[2])
	}
	// Non-draft events are ignored.
	if r := b.RunHooks(context.Background(), events.Event{Type: events.MailBeforeSend}); len(r.Findings) != 0 {
		t.Fatal(r)
	}
}
