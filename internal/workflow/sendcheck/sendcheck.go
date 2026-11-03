// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sendcheck implements TuxMail's built-in pre-send checks
// (KONZEPT 8.13). Every check is a pure function over a draft
// (events.Draft), a configuration and optional lookups; Run executes all
// enabled checks and Hook adapts them to the mail.before_send bus hook.
//
// Each finding carries a severity (info, warn, block — configurable per
// check), an i18n message key ("sendcheck.<check>") and German and English
// default texts.
package sendcheck

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/events"
)

// Finding is a check result (shared with the event bus).
type Finding = events.Finding

// Severity grades a finding.
type Severity = events.Severity

// Severities.
const (
	Info  = events.SeverityInfo
	Warn  = events.SeverityWarn
	Block = events.SeverityBlock
)

// CheckID names a check.
type CheckID string

// Built-in checks.
const (
	NoRecipients            CheckID = "no_recipients"
	ExternalRecipients      CheckID = "external_recipients"
	ManyExternal            CheckID = "many_external_recipients"
	PrivateAddress          CheckID = "private_address"
	WrongIdentity           CheckID = "wrong_identity"
	ForgottenAttachment     CheckID = "forgotten_attachment"
	AttachmentTooLarge      CheckID = "attachment_too_large"
	MissingSubject          CheckID = "missing_subject"
	EmptyBody               CheckID = "empty_body"
	UnreplacedPlaceholder   CheckID = "unreplaced_placeholder"
	PGPKeyAvailable         CheckID = "pgp_key_available"
	SensitiveData           CheckID = "sensitive_data"
	ConfidentialUnencrypted CheckID = "confidential_unencrypted"
	ReplyAllLarge           CheckID = "reply_all_large"
	OldConversation         CheckID = "old_conversation"
	OutsideWorkingHours     CheckID = "outside_working_hours"
)

// DefaultFreemailDomains are common private mail providers.
var DefaultFreemailDomains = []string{
	"gmail.com", "googlemail.com", "gmx.de", "gmx.net", "gmx.at", "gmx.ch", "gmx.com", "web.de",
	"t-online.de", "outlook.com", "outlook.de", "hotmail.com", "hotmail.de", "live.com", "live.de", "msn.com",
	"yahoo.com", "yahoo.de", "ymail.com", "icloud.com", "me.com", "mac.com", "posteo.de", "posteo.net",
	"mailbox.org", "proton.me", "protonmail.com", "protonmail.ch", "pm.me", "freenet.de", "aol.com", "aol.de",
}

// DefaultAttachmentKeywords trigger the forgotten-attachment check (DE/EN).
var DefaultAttachmentKeywords = []string{
	"anbei", "im anhang", "in der anlage", "als anlage", "angehängt", "anhängend", "beigefügt", "beiliegend",
	"siehe anhang", "anlage:", "attached", "attachment", "enclosed", "please find",
}

// DefaultConfidentialKeywords trigger the confidential-unencrypted check.
var DefaultConfidentialKeywords = []string{
	"vertraulich", "streng geheim", "gehalt", "gehaltsabrechnung", "personalakte", "diagnose",
	"confidential", "salary", "payslip", "strictly private",
}

// WorkingHours describes when recipients are expected to work.
type WorkingHours struct {
	Start int            // hour, inclusive (default 8)
	End   int            // hour, exclusive (default 18)
	Days  []time.Weekday // default Monday-Friday
	// Location is used for recipients without a known time zone (default time.Local).
	Location *time.Location
}

// Config configures the checks.
type Config struct {
	// OwnDomains are the organization's domains; recipients elsewhere are
	// external, identities in them are business identities.
	OwnDomains       []string
	FreemailDomains  []string
	ManyExternalMin  int   // default 5
	LargeListMin     int   // reply-all warning threshold, default 10
	MaxAttachment    int64 // per-file size above which a cloud link is suggested, default 10 MiB
	OldConversation  time.Duration
	WorkingHours     WorkingHours
	AttachmentWords  []string
	ConfidentialWord []string
	// Disabled checks are skipped.
	Disabled map[CheckID]bool
	// Severity overrides the default severity of a check (e.g. make a warning a block).
	Severity map[CheckID]Severity
}

// DefaultConfig returns the configuration with all defaults filled in.
func DefaultConfig() Config {
	c := Config{}
	c.fill()
	return c
}

func (c *Config) fill() {
	if c.FreemailDomains == nil {
		c.FreemailDomains = DefaultFreemailDomains
	}
	if c.ManyExternalMin <= 0 {
		c.ManyExternalMin = 5
	}
	if c.LargeListMin <= 0 {
		c.LargeListMin = 10
	}
	if c.MaxAttachment <= 0 {
		c.MaxAttachment = 10 << 20
	}
	if c.OldConversation <= 0 {
		c.OldConversation = 183 * 24 * time.Hour
	}
	if c.WorkingHours.End <= c.WorkingHours.Start {
		c.WorkingHours.Start, c.WorkingHours.End = 8, 18
	}
	if c.WorkingHours.Days == nil {
		c.WorkingHours.Days = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	}
	if c.WorkingHours.Location == nil {
		c.WorkingHours.Location = time.Local
	}
	if c.AttachmentWords == nil {
		c.AttachmentWords = DefaultAttachmentKeywords
	}
	if c.ConfidentialWord == nil {
		c.ConfidentialWord = DefaultConfidentialKeywords
	}
}

// KeyLookup answers whether a usable OpenPGP key is known for an address.
type KeyLookup interface {
	HasKey(email string) bool
}

// ContactLookup classifies recipients by the address book.
type ContactLookup interface {
	// IsBusinessContact reports whether email belongs to a business contact;
	// known is false if the address is not in the address book.
	IsBusinessContact(email string) (business, known bool)
}

// Deps are optional lookups; nil lookups disable the checks that need them.
type Deps struct {
	Keys     KeyLookup
	Contacts ContactLookup
	// RecipientLocation returns a recipient's time zone (nil = unknown).
	RecipientLocation func(email string) *time.Location
}

// Input is what a check sees.
type Input struct {
	Draft  *events.Draft
	Config *Config
	Deps   Deps
	Now    time.Time
}

// Check is a single pre-send check.
type Check struct {
	ID       CheckID
	Severity Severity // default severity
	Fn       func(in *Input, sev Severity) []Finding
}

// Checks lists all built-in checks in display order.
var Checks = []Check{
	{NoRecipients, Block, checkNoRecipients},
	{MissingSubject, Warn, checkMissingSubject},
	{EmptyBody, Warn, checkEmptyBody},
	{UnreplacedPlaceholder, Warn, checkPlaceholders},
	{ForgottenAttachment, Warn, checkForgottenAttachment},
	{AttachmentTooLarge, Info, checkAttachmentSize},
	{ExternalRecipients, Info, checkExternal},
	{ManyExternal, Warn, checkManyExternal},
	{PrivateAddress, Warn, checkPrivateAddress},
	{WrongIdentity, Warn, checkWrongIdentity},
	{PGPKeyAvailable, Info, checkPGPKey},
	{SensitiveData, Warn, checkSensitive},
	{ConfidentialUnencrypted, Info, checkConfidential},
	{ReplyAllLarge, Warn, checkReplyAll},
	{OldConversation, Info, checkOldConversation},
	{OutsideWorkingHours, Info, checkWorkingHours},
}

// Run executes all enabled checks and returns their findings, sorted by
// descending severity (stable).
func Run(d *events.Draft, cfg Config, deps Deps, now time.Time) []Finding {
	cfg.fill()
	in := &Input{Draft: d, Config: &cfg, Deps: deps, Now: now}
	var out []Finding
	for _, c := range Checks {
		if cfg.Disabled[c.ID] {
			continue
		}
		sev := c.Severity
		if s, ok := cfg.Severity[c.ID]; ok && s.Valid() {
			sev = s
		}
		out = append(out, c.Fn(in, sev)...)
	}
	sortFindings(out)
	return out
}

func sortFindings(fs []Finding) {
	// insertion sort keeps it stable and dependency-free for short lists
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && fs[j].Severity.Rank() > fs[j-1].Severity.Rank(); j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
}

// Hook adapts the checks to an events.Hook for mail.before_send. config is
// called for every draft so that settings changes apply immediately.
func Hook(config func() Config, deps Deps, now func() time.Time) events.Hook {
	if now == nil {
		now = time.Now
	}
	return func(_ context.Context, ev events.Event) (events.HookResult, error) {
		if ev.Draft == nil {
			return events.HookResult{}, nil
		}
		fs := Run(ev.Draft, config(), deps, now())
		res := events.HookResult{Findings: fs}
		for _, f := range fs {
			if f.Severity == Block {
				res.Vetoed = true
			}
		}
		return res, nil
	}
}

func finding(id CheckID, sev Severity, de, en string, params map[string]string) Finding {
	return Finding{Check: string(id), Severity: sev, Key: "sendcheck." + string(id), DE: de, EN: en, Params: params}
}

// ---- helpers ----

func domainOf(addr string) string {
	if i := strings.LastIndexByte(addr, '@'); i >= 0 {
		return strings.ToLower(strings.TrimSpace(addr[i+1:]))
	}
	return ""
}

func inDomains(addr string, domains []string) bool {
	d := domainOf(addr)
	if d == "" {
		return false
	}
	for _, x := range domains {
		x = strings.ToLower(strings.TrimPrefix(x, "@"))
		if d == x || strings.HasSuffix(d, "."+x) {
			return true
		}
	}
	return false
}

func (in *Input) businessIdentity() bool {
	id := in.Draft.Identity
	if id.Business != nil {
		return *id.Business
	}
	return len(in.Config.OwnDomains) > 0 && inDomains(id.Email, in.Config.OwnDomains)
}

func (in *Input) privateIdentity() bool {
	id := in.Draft.Identity
	if id.Business != nil {
		return !*id.Business
	}
	return inDomains(id.Email, in.Config.FreemailDomains)
}

func addrList(as []domain.Address) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		if e := strings.TrimSpace(a.Email); e != "" {
			out = append(out, strings.ToLower(e))
		}
	}
	return out
}

func joinMax(list []string, n int) string {
	if len(list) <= n {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:n], ", ") + fmt.Sprintf(" (+%d)", len(list)-n)
}

// newText returns the part of the body written by the user: quoted lines and
// everything after a quote header are removed.
var quoteHeader = regexp.MustCompile(`(?im)^(-{2,}\s*(original message|ursprüngliche nachricht|forwarded message|weitergeleitete nachricht)|(am|on) .{3,120}(schrieb|wrote)[^\n]*:\s*$)`)

func newText(body string) string {
	if loc := quoteHeader.FindStringIndex(body); loc != nil {
		body = body[:loc[0]]
	}
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// ---- checks ----

func checkNoRecipients(in *Input, sev Severity) []Finding {
	if len(addrList(in.Draft.Recipients())) > 0 {
		return nil
	}
	return []Finding{finding(NoRecipients, sev, "Die Nachricht hat keine Empfänger.", "The message has no recipients.", nil)}
}

func checkMissingSubject(in *Input, sev Severity) []Finding {
	if strings.TrimSpace(in.Draft.Subject) != "" {
		return nil
	}
	return []Finding{finding(MissingSubject, sev, "Der Betreff fehlt.", "The subject is missing.", nil)}
}

func checkEmptyBody(in *Input, sev Severity) []Finding {
	if strings.TrimSpace(newText(in.Draft.Body)) != "" || len(in.Draft.Attachments) > 0 {
		return nil
	}
	return []Finding{finding(EmptyBody, sev, "Die Nachricht enthält keinen Text.", "The message is empty.", nil)}
}

var placeholderRe = regexp.MustCompile(`\{\{\s*[\p{L}_][\p{L}\p{N}_.\-]*\s*\}\}`)

func checkPlaceholders(in *Input, sev Severity) []Finding {
	found := placeholderRe.FindAllString(in.Draft.Subject+"\n"+newText(in.Draft.Body), -1)
	if len(found) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var uniq []string
	for _, f := range found {
		if !seen[f] {
			seen[f] = true
			uniq = append(uniq, f)
		}
	}
	list := joinMax(uniq, 5)
	return []Finding{finding(UnreplacedPlaceholder, sev,
		"Nicht ersetzte Platzhalter: "+list, "Unreplaced placeholders: "+list, map[string]string{"placeholders": list})}
}

func checkForgottenAttachment(in *Input, sev Severity) []Finding {
	if len(in.Draft.Attachments) > 0 {
		return nil
	}
	text := strings.ToLower(in.Draft.Subject + "\n" + newText(in.Draft.Body))
	for _, w := range in.Config.AttachmentWords {
		if w = strings.ToLower(w); w != "" && containsWord(text, w) {
			return []Finding{finding(ForgottenAttachment, sev,
				fmt.Sprintf("Der Text erwähnt einen Anhang („%s“), es ist aber keiner angehängt.", w),
				fmt.Sprintf("The text mentions an attachment (%q) but nothing is attached.", w),
				map[string]string{"keyword": w})}
		}
	}
	return nil
}

// containsWord matches w at word boundaries (letters/digits around it end a match).
func containsWord(text, w string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], w)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(w)
		if (start == 0 || !isWordByte(text[start-1])) && (end == len(text) || !isWordByte(text[end])) {
			return true
		}
		i = start + 1
	}
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b >= 0x80
}

func checkAttachmentSize(in *Input, sev Severity) []Finding {
	var big []string
	for _, a := range in.Draft.Attachments {
		if a.Size > in.Config.MaxAttachment {
			big = append(big, a.Name)
		}
	}
	if len(big) == 0 {
		return nil
	}
	list := joinMax(big, 3)
	limit := strconv.FormatInt(in.Config.MaxAttachment>>20, 10)
	return []Finding{finding(AttachmentTooLarge, sev,
		fmt.Sprintf("Anhang größer als %s MB (%s) – als Cloud-Link senden?", limit, list),
		fmt.Sprintf("Attachment larger than %s MB (%s) – send as cloud link?", limit, list),
		map[string]string{"files": list, "limitMB": limit})}
}

func (in *Input) external(as []domain.Address) []string {
	if len(in.Config.OwnDomains) == 0 {
		return nil
	}
	var out []string
	for _, a := range addrList(as) {
		if !inDomains(a, in.Config.OwnDomains) {
			out = append(out, a)
		}
	}
	return out
}

func checkExternal(in *Input, sev Severity) []Finding {
	if !in.businessIdentity() {
		return nil
	}
	ext := in.external(in.Draft.Recipients())
	if len(ext) == 0 {
		return nil
	}
	list := joinMax(ext, 3)
	n := strconv.Itoa(len(ext))
	return []Finding{finding(ExternalRecipients, sev,
		fmt.Sprintf("%s Empfänger außerhalb der eigenen Organisation: %s", n, list),
		fmt.Sprintf("%s recipient(s) outside your organization: %s", n, list),
		map[string]string{"count": n, "recipients": list})}
}

func checkManyExternal(in *Input, sev Severity) []Finding {
	visible := append(append([]domain.Address{}, in.Draft.To...), in.Draft.Cc...)
	var ext []string
	if len(in.Config.OwnDomains) > 0 {
		ext = in.external(visible)
	} else {
		// Without own domains every visible recipient of another domain than
		// the sender counts.
		own := domainOf(in.Draft.Identity.Email)
		for _, a := range addrList(visible) {
			if domainOf(a) != own {
				ext = append(ext, a)
			}
		}
	}
	if len(ext) < in.Config.ManyExternalMin {
		return nil
	}
	n := strconv.Itoa(len(ext))
	return []Finding{finding(ManyExternal, sev,
		fmt.Sprintf("%s externe Empfänger in An/CC sehen sich gegenseitig – BCC verwenden?", n),
		fmt.Sprintf("%s external recipients in To/Cc can see each other – use Bcc?", n),
		map[string]string{"count": n})}
}

func checkPrivateAddress(in *Input, sev Severity) []Finding {
	if !in.businessIdentity() {
		return nil
	}
	var priv []string
	for _, a := range addrList(in.Draft.Recipients()) {
		if inDomains(a, in.Config.FreemailDomains) {
			priv = append(priv, a)
		}
	}
	if len(priv) == 0 {
		return nil
	}
	list := joinMax(priv, 3)
	return []Finding{finding(PrivateAddress, sev,
		"Private Adresse(n) in einer dienstlichen Mail: "+list,
		"Private address(es) in a business mail: "+list,
		map[string]string{"recipients": list})}
}

func checkWrongIdentity(in *Input, sev Severity) []Finding {
	if !in.privateIdentity() {
		return nil
	}
	var biz []string
	for _, a := range addrList(in.Draft.Recipients()) {
		isBiz := len(in.Config.OwnDomains) > 0 && inDomains(a, in.Config.OwnDomains)
		if !isBiz && in.Deps.Contacts != nil {
			if b, known := in.Deps.Contacts.IsBusinessContact(a); known && b {
				isBiz = true
			}
		}
		if isBiz {
			biz = append(biz, a)
		}
	}
	if len(biz) == 0 {
		return nil
	}
	list := joinMax(biz, 3)
	from := in.Draft.Identity.Email
	return []Finding{finding(WrongIdentity, sev,
		fmt.Sprintf("Geschäftliche Empfänger (%s), aber privater Absender %s – richtige Identität?", list, from),
		fmt.Sprintf("Business recipients (%s) but private sender %s – wrong identity?", list, from),
		map[string]string{"recipients": list, "identity": from})}
}

func checkPGPKey(in *Input, sev Severity) []Finding {
	if in.Draft.Encrypt || in.Deps.Keys == nil {
		return nil
	}
	recips := addrList(in.Draft.Recipients())
	var with []string
	for _, a := range recips {
		if in.Deps.Keys.HasKey(a) {
			with = append(with, a)
		}
	}
	if len(with) == 0 {
		return nil
	}
	list := joinMax(with, 3)
	if len(with) == len(recips) {
		return []Finding{finding(PGPKeyAvailable, sev,
			"Alle Empfänger haben einen PGP-Schlüssel, die Mail wird aber unverschlüsselt gesendet.",
			"All recipients have a PGP key, but the mail is sent unencrypted.",
			map[string]string{"recipients": list, "all": "true"})}
	}
	return []Finding{finding(PGPKeyAvailable, sev,
		"PGP-Schlüssel vorhanden für: "+list+" – die Mail ist unverschlüsselt.",
		"PGP key available for: "+list+" – the mail is not encrypted.",
		map[string]string{"recipients": list, "all": "false"})}
}

func checkSensitive(in *Input, sev Severity) []Finding {
	if in.Draft.Encrypt {
		return nil
	}
	text := in.Draft.Subject + "\n" + newText(in.Draft.Body)
	var out []Finding
	if ibans := FindIBANs(text); len(ibans) > 0 {
		m := make([]string, len(ibans))
		for i, s := range ibans {
			m[i] = mask(s)
		}
		list := joinMax(m, 3)
		out = append(out, finding(SensitiveData, sev,
			"IBAN im unverschlüsselten Text: "+list, "IBAN in unencrypted text: "+list,
			map[string]string{"kind": "iban", "values": list}))
	}
	if cards := FindCardNumbers(text); len(cards) > 0 {
		m := make([]string, len(cards))
		for i, s := range cards {
			m[i] = mask(s)
		}
		list := joinMax(m, 3)
		out = append(out, finding(SensitiveData, sev,
			"Kreditkartennummer im unverschlüsselten Text: "+list, "Credit card number in unencrypted text: "+list,
			map[string]string{"kind": "credit_card", "values": list}))
	}
	if pw := FindPasswords(text); len(pw) > 0 {
		out = append(out, finding(SensitiveData, sev,
			"Die Mail scheint ein Passwort im Klartext zu enthalten.", "The mail seems to contain a plain-text password.",
			map[string]string{"kind": "password", "label": pw[0]}))
	}
	return out
}

func checkConfidential(in *Input, sev Severity) []Finding {
	if in.Draft.Encrypt {
		return nil
	}
	text := strings.ToLower(in.Draft.Subject + "\n" + newText(in.Draft.Body))
	for _, w := range in.Config.ConfidentialWord {
		if w = strings.ToLower(w); w != "" && containsWord(text, w) {
			return []Finding{finding(ConfidentialUnencrypted, sev,
				fmt.Sprintf("Vertraulicher Inhalt („%s“) ohne Verschlüsselung.", w),
				fmt.Sprintf("Confidential content (%q) without encryption.", w),
				map[string]string{"keyword": w})}
		}
	}
	return nil
}

func checkReplyAll(in *Input, sev Severity) []Finding {
	if !in.Draft.ReplyAll {
		return nil
	}
	n := len(addrList(in.Draft.Recipients()))
	if n < in.Config.LargeListMin {
		return nil
	}
	s := strconv.Itoa(n)
	return []Finding{finding(ReplyAllLarge, sev,
		fmt.Sprintf("„Allen antworten“ an %s Empfänger – wirklich an alle?", s),
		fmt.Sprintf("Reply all to %s recipients – really everyone?", s),
		map[string]string{"count": s})}
}

func checkOldConversation(in *Input, sev Severity) []Finding {
	d := in.Draft.InReplyToDate
	if d.IsZero() || in.Now.Sub(d) <= in.Config.OldConversation {
		return nil
	}
	months := strconv.Itoa(int(in.Now.Sub(d).Hours() / 24 / 30))
	return []Finding{finding(OldConversation, sev,
		fmt.Sprintf("Antwort auf eine Konversation, die %s Monate alt ist.", months),
		fmt.Sprintf("Reply to a conversation that is %s months old.", months),
		map[string]string{"months": months, "date": d.Format("2006-01-02")})}
}

func (wh WorkingHours) contains(t time.Time) bool {
	working := false
	for _, d := range wh.Days {
		working = working || t.Weekday() == d
	}
	return working && t.Hour() >= wh.Start && t.Hour() < wh.End
}

func checkWorkingHours(in *Input, sev Severity) []Finding {
	at := in.Draft.SendAt
	if at.IsZero() {
		at = in.Now
	}
	wh := in.Config.WorkingHours
	recips := addrList(in.Draft.Recipients())
	if len(recips) == 0 {
		return nil
	}
	// Only warn if it is outside working hours for every recipient.
	for _, r := range recips {
		loc := wh.Location
		if in.Deps.RecipientLocation != nil {
			if l := in.Deps.RecipientLocation(r); l != nil {
				loc = l
			}
		}
		if wh.contains(at.In(loc)) {
			return nil
		}
	}
	local := at.In(wh.Location).Format("Mon 15:04")
	return []Finding{finding(OutsideWorkingHours, sev,
		fmt.Sprintf("Versand außerhalb der Arbeitszeit der Empfänger (%s) – später senden?", local),
		fmt.Sprintf("Sending outside the recipients' working hours (%s) – send later?", local),
		map[string]string{"time": local})}
}
