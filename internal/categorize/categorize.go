// SPDX-License-Identifier: AGPL-3.0-or-later

// Package categorize sorts incoming mail into smart categories locally
// (KONZEPT 8.8): Wichtig/Persönlich, Newsletter, Rechnungen & Bestellungen,
// Benachrichtigungen, Social and Termine.
//
// Classification combines rules/heuristics (List-Id, Precedence, sender
// domain lists, German and English keywords, invoice attachments, calendar
// parts, social network and notification senders) with a multinomial naive
// Bayes model that is retrained whenever the user moves a mail to another
// category. An optional Refiner (e.g. the AI assistant, KONZEPT 8.10) may
// override uncertain results.
//
// Categories are stored as local item categories (store table
// message_categories), not as tags: they are never synced to the server.
package categorize

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Category ids (stable, used in search "category:" and workflows).
const (
	Personal      = "personal"
	Newsletter    = "newsletter"
	Invoice       = "invoice"
	Notification  = "notification"
	Social        = "social"
	Appointment   = "appointment"
	DefaultResult = Personal
)

// All lists the categories in display order.
var All = []string{Personal, Newsletter, Invoice, Notification, Social, Appointment}

// Aliases maps search/workflow spellings (de/en) to category ids.
var Aliases = map[string]string{
	"personal": Personal, "persönlich": Personal, "persoenlich": Personal, "wichtig": Personal, "important": Personal, "primary": Personal,
	"newsletter": Newsletter, "newsletters": Newsletter, "promotions": Newsletter, "werbung": Newsletter,
	"invoice": Invoice, "invoices": Invoice, "rechnung": Invoice, "rechnungen": Invoice, "bestellung": Invoice, "bestellungen": Invoice, "orders": Invoice, "order": Invoice,
	"notification": Notification, "notifications": Notification, "benachrichtigung": Notification, "benachrichtigungen": Notification, "updates": Notification,
	"social": Social, "sozial": Social,
	"appointment": Appointment, "appointments": Appointment, "termin": Appointment, "termine": Appointment, "calendar": Appointment, "events": Appointment,
}

// Normalize maps an alias to a category id ("" if unknown).
func Normalize(s string) string {
	return Aliases[strings.ToLower(strings.TrimSpace(s))]
}

// Mail is the classifier's view of a message.
type Mail struct {
	From    string
	Subject string
	Text    string
	Headers map[string][]string
	// AttachmentNames / AttachmentTypes of all attachments.
	AttachmentNames []string
	AttachmentTypes []string
	HasCalendar     bool
	// IsInvoice is true when a structured e-invoice was found.
	IsInvoice bool
	// KnownContact is true when the sender is in the address book or the
	// user has written to them before.
	KnownContact bool
}

func (m Mail) header(name string) string {
	for k, v := range m.Headers {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// Result is a classification.
type Result struct {
	Category   string             `json:"category"`
	Confidence float64            `json:"confidence"` // 0..1
	Source     string             `json:"source"`     // rule | model | ai | user
	Scores     map[string]float64 `json:"scores,omitempty"`
	Reasons    []string           `json:"reasons,omitempty"`
}

// ModelStore persists the category model (implemented by *store.Store).
type ModelStore interface {
	// CategoryCounts returns, per category, the token counts of the given
	// tokens; docs returns the number of training documents per category.
	CategoryCounts(ctx context.Context, tokens []string) (counts map[string]map[string]int, docs map[string]int, err error)
	CategoryTrain(ctx context.Context, category string, tokens []string, delta int) error
}

// Refiner may refine uncertain results (optional AI hook, KONZEPT 8.10).
// It must only run on local models or after the user allowed it.
type Refiner interface {
	Refine(ctx context.Context, m Mail, r Result) (Result, error)
}

// Classifier categorizes mail.
type Classifier struct {
	Model   ModelStore // optional
	Refiner Refiner    // optional
	// RefineBelow is the confidence below which the Refiner is asked (default 0.6).
	RefineBelow float64
}

// Classify categorizes a mail.
func (c *Classifier) Classify(ctx context.Context, m Mail) (Result, error) {
	scores, reasons := ruleScores(m)
	src := "rule"
	if c.Model != nil {
		toks := Tokens(m)
		counts, docs, err := c.Model.CategoryCounts(ctx, toks)
		if err != nil {
			return Result{}, err
		}
		if probs, ok := bayes(toks, counts, docs); ok {
			for k, p := range probs {
				scores[k] += 2.5 * p
			}
			src = "model"
		}
	}
	best, total := DefaultResult, 0.0
	for _, k := range All {
		total += scores[k]
	}
	for _, k := range All {
		if scores[k] > scores[best] {
			best = k
		}
	}
	conf := 0.5
	if total > 0 {
		conf = scores[best] / total
	}
	if scores[best] < 1 && best != Personal {
		// weak evidence: fall back to personal
		best, conf = Personal, 0.4
	}
	r := Result{Category: best, Confidence: math.Round(conf*100) / 100, Source: src, Scores: scores, Reasons: reasons[best]}
	limit := c.RefineBelow
	if limit == 0 {
		limit = 0.6
	}
	if c.Refiner != nil && r.Confidence < limit {
		if rr, err := c.Refiner.Refine(ctx, m, r); err == nil && Normalize(rr.Category) != "" {
			rr.Category = Normalize(rr.Category)
			if rr.Source == "" {
				rr.Source = "ai"
			}
			r = rr
		}
	}
	return r, nil
}

// Learn retrains the model when the user moves a mail from one category to
// another (from may be "").
func (c *Classifier) Learn(ctx context.Context, m Mail, from, to string) error {
	if c.Model == nil {
		return nil
	}
	toks := Tokens(m)
	if from != "" && from != to {
		if err := c.Model.CategoryTrain(ctx, from, toks, -1); err != nil {
			return err
		}
	}
	return c.Model.CategoryTrain(ctx, to, toks, 1)
}

// ==== rules ====================================================================

var (
	socialDomains = []string{"facebook.com", "facebookmail.com", "instagram.com", "linkedin.com", "twitter.com", "x.com",
		"xing.com", "mastodon.social", "tiktok.com", "pinterest.com", "reddit.com", "redditmail.com", "youtube.com",
		"meetup.com", "discord.com", "snapchat.com", "threads.net", "bsky.app", "strava.com"}
	notifyDomains = []string{"github.com", "gitlab.com", "atlassian.net", "atlassian.com", "slack.com", "notion.so",
		"google.com", "accounts.google.com", "microsoft.com", "apple.com", "id.apple.com", "dropbox.com", "nextcloud.com",
		"bitbucket.org", "sentry.io", "trello.com", "asana.com", "zoom.us", "dhl.de", "dhl.com", "dpd.de", "hermesworld.com", "ups.com"}
	shopDomains = []string{"amazon.de", "amazon.com", "ebay.de", "ebay.com", "paypal.de", "paypal.com", "otto.de", "zalando.de",
		"telekom.de", "vodafone.de", "o2online.de", "1und1.de", "ionos.de", "stripe.com", "apple.com", "booking.com", "bahn.de", "lieferando.de"}
	notifyLocal = []string{"noreply", "no-reply", "donotreply", "do-not-reply", "notification", "notifications", "notify",
		"alert", "alerts", "mailer-daemon", "postmaster", "system", "automated", "info-noreply", "security", "account"}
	newsLocal = []string{"newsletter", "news", "marketing", "promo", "promotion", "deals", "offers", "angebote", "aktion", "mailing"}

	invoiceKW = []string{"rechnung", "invoice", "receipt", "quittung", "beleg", "zahlungsbestätigung", "zahlungseingang",
		"bestellung", "bestellbestätigung", "order confirmation", "your order", "ihre bestellung", "auftragsbestätigung",
		"payment received", "zahlungserinnerung", "mahnung", "gutschrift", "credit note", "billing", "abrechnung", "lastschrift", "versandbestätigung", "shipped"}
	newsKW = []string{"newsletter", "unsubscribe", "abmelden", "abbestellen", "angebot", "rabatt", "sale", "% off", "gutschein",
		"coupon", "deal", "jetzt kaufen", "shop now", "webinar", "neuigkeiten", "news", "edition", "ausgabe", "im browser ansehen", "view in browser"}
	notifyKW = []string{"notification", "benachrichtigung", "password reset", "passwort zurücksetzen", "verify your", "bestätigen sie",
		"security alert", "sicherheitswarnung", "sign-in", "anmeldung", "login", "code", "verification", "your account", "ihr konto",
		"pull request", "merge request", "commented", "build failed", "pipeline", "reminder", "erinnerung", "delivery", "zustellung", "sendung"}
	socialKW = []string{"friend request", "freundschaftsanfrage", "mentioned you", "hat dich erwähnt", "new follower", "neuer follower",
		"connection request", "kontaktanfrage", "liked your", "gefällt", "tagged you", "markiert", "invited you to connect", "endorsed"}
	apptKW = []string{"einladung", "invitation", "termin", "meeting", "besprechung", "appointment", "accepted:", "angenommen:", "declined:",
		"abgelehnt:", "updated invitation", "aktualisierte einladung", "calendar", "kalender", "terminbestätigung", "booking confirmation", "reservierung", "reservation"}
	personalKW = []string{"hallo", "hi ", "liebe ", "lieber ", "dear ", "viele grüße", "liebe grüße", "lg", "best regards", "cheers", "danke", "thanks"}

	invoiceFileRe = regexp.MustCompile(`(?i)(rechnung|invoice|receipt|quittung|beleg|faktura|bill|gutschrift|re[-_ ]?\d)`)
)

func domainOf(addr string) (local, dom string) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	i := strings.LastIndexByte(addr, '@')
	if i < 0 {
		return addr, ""
	}
	return addr[:i], addr[i+1:]
}

func domainIn(dom string, list []string) bool {
	for _, d := range list {
		if dom == d || strings.HasSuffix(dom, "."+d) {
			return true
		}
	}
	return false
}

func countKW(text string, kws []string) (int, string) {
	n, first := 0, ""
	for _, k := range kws {
		if strings.Contains(text, k) {
			n++
			if first == "" {
				first = k
			}
		}
	}
	return n, first
}

// ruleScores returns heuristic evidence per category plus reasons.
func ruleScores(m Mail) (map[string]float64, map[string][]string) {
	s := map[string]float64{}
	why := map[string][]string{}
	add := func(cat string, w float64, reason string) {
		s[cat] += w
		why[cat] = append(why[cat], reason)
	}
	local, dom := domainOf(m.From)
	subj := strings.ToLower(m.Subject)
	body := strings.ToLower(m.Text)
	if len(body) > 20000 {
		body = body[:20000]
	}
	listID := m.header("List-Id")
	prec := strings.ToLower(strings.TrimSpace(m.header("Precedence")))
	unsub := m.header("List-Unsubscribe")
	autoSub := strings.ToLower(m.header("Auto-Submitted"))

	if m.HasCalendar {
		add(Appointment, 4, "calendar part")
	}
	if m.IsInvoice {
		add(Invoice, 5, "structured e-invoice")
	}
	for i, n := range m.AttachmentNames {
		t := ""
		if i < len(m.AttachmentTypes) {
			t = strings.ToLower(m.AttachmentTypes[i])
		}
		ln := strings.ToLower(n)
		if strings.HasSuffix(ln, ".ics") || t == "text/calendar" || t == "application/ics" {
			add(Appointment, 3, "calendar attachment")
		}
		if (strings.HasSuffix(ln, ".pdf") || strings.HasSuffix(ln, ".xml")) && invoiceFileRe.MatchString(ln) {
			add(Invoice, 3, "invoice attachment "+n)
		}
	}
	if domainIn(dom, socialDomains) {
		add(Social, 3, "social network sender")
	}
	if domainIn(dom, shopDomains) {
		add(Invoice, 0.8, "shop/provider sender")
	}
	if domainIn(dom, notifyDomains) {
		add(Notification, 1.5, "service sender")
	}
	for _, l := range notifyLocal {
		if local == l || strings.HasPrefix(local, l+"-") || strings.HasPrefix(local, l+".") || strings.HasPrefix(local, l+"+") {
			add(Notification, 1.5, "automated sender address")
			break
		}
	}
	for _, l := range newsLocal {
		if strings.Contains(local, l) {
			add(Newsletter, 2, "newsletter sender address")
			break
		}
	}
	listW := 2.0
	if domainIn(dom, notifyDomains) || domainIn(dom, socialDomains) {
		listW = 0.7 // services send notifications through list software
	}
	if listID != "" {
		add(Newsletter, listW, "List-Id")
	}
	if unsub != "" {
		add(Newsletter, listW, "List-Unsubscribe")
	}
	if prec == "bulk" || prec == "list" {
		add(Newsletter, 1, "Precedence: "+prec)
	}
	if prec == "junk" {
		add(Newsletter, 1, "Precedence: junk")
	}
	if autoSub != "" && autoSub != "no" {
		add(Notification, 1.5, "Auto-Submitted")
	}
	if n, k := countKW(subj, invoiceKW); n > 0 {
		add(Invoice, 2+float64(n-1)*0.5, "keyword "+k)
	}
	if n, k := countKW(body, invoiceKW); n > 0 {
		add(Invoice, math.Min(float64(n)*0.5, 2), "body keyword "+k)
	}
	if n, k := countKW(subj, apptKW); n > 0 {
		add(Appointment, 1.5+float64(n-1)*0.5, "keyword "+k)
	}
	if n, k := countKW(subj, notifyKW); n > 0 {
		add(Notification, 1+float64(n-1)*0.5, "keyword "+k)
	}
	if n, k := countKW(subj+" "+body, socialKW); n > 0 {
		add(Social, 1.5*float64(n), "keyword "+k)
	}
	if n, k := countKW(subj, newsKW); n > 0 {
		add(Newsletter, float64(n), "keyword "+k)
	}
	if n, k := countKW(body, newsKW); n > 0 {
		add(Newsletter, math.Min(float64(n)*0.4, 1.6), "body keyword "+k)
	}
	if m.KnownContact {
		add(Personal, 3, "known contact")
	}
	if listID == "" && unsub == "" && prec == "" && (autoSub == "" || autoSub == "no") && !domainIn(dom, notifyDomains) {
		add(Personal, 1, "no list headers")
		if n, _ := countKW(body, personalKW); n > 0 {
			add(Personal, math.Min(float64(n)*0.5, 1.5), "personal greeting")
		}
	}
	return s, why
}

// ==== model ====================================================================

// Tokens returns the model features of a mail.
func Tokens(m Mail) []string {
	set := map[string]bool{}
	_, dom := domainOf(m.From)
	if dom != "" {
		set["from:"+dom] = true
	}
	for _, w := range splitWords(m.Subject) {
		set["s:"+w] = true
	}
	n := 0
	for _, w := range splitWords(m.Text) {
		if n > 400 {
			break
		}
		if !set[w] {
			set[w] = true
			n++
		}
	}
	if m.header("List-Id") != "" {
		set["h:list-id"] = true
	}
	if m.header("List-Unsubscribe") != "" {
		set["h:list-unsubscribe"] = true
	}
	if m.HasCalendar {
		set["h:calendar"] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func splitWords(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if l := len([]rune(f)); l >= 3 && l <= 24 {
			out = append(out, f)
		}
	}
	return out
}

// bayes returns normalized category probabilities; ok is false without
// enough training (fewer than 3 documents in total).
func bayes(toks []string, counts map[string]map[string]int, docs map[string]int) (map[string]float64, bool) {
	totalDocs := 0
	for _, d := range docs {
		totalDocs += d
	}
	if totalDocs < 3 {
		return nil, false
	}
	logp := map[string]float64{}
	for _, cat := range All {
		d := docs[cat]
		if d == 0 {
			continue
		}
		lp := math.Log(float64(d) / float64(totalDocs))
		for _, t := range toks {
			c := counts[cat][t]
			// Bernoulli-ish smoothing over documents.
			lp += math.Log((float64(c) + 0.5) / (float64(d) + 1))
		}
		logp[cat] = lp
	}
	if len(logp) == 0 {
		return nil, false
	}
	maxL := math.Inf(-1)
	for _, v := range logp {
		maxL = math.Max(maxL, v)
	}
	sum := 0.0
	out := map[string]float64{}
	for k, v := range logp {
		out[k] = math.Exp(v - maxL)
		sum += out[k]
	}
	for k := range out {
		out[k] /= sum
	}
	return out, true
}

// MemModel is an in-memory ModelStore (tests, demo).
type MemModel struct {
	Counts map[string]map[string]int
	Docs   map[string]int
}

// NewMemModel returns an empty MemModel.
func NewMemModel() *MemModel {
	return &MemModel{Counts: map[string]map[string]int{}, Docs: map[string]int{}}
}

// CategoryCounts implements ModelStore.
func (m *MemModel) CategoryCounts(_ context.Context, toks []string) (map[string]map[string]int, map[string]int, error) {
	out := map[string]map[string]int{}
	for cat, c := range m.Counts {
		out[cat] = map[string]int{}
		for _, t := range toks {
			if n := c[t]; n > 0 {
				out[cat][t] = n
			}
		}
	}
	docs := map[string]int{}
	for k, v := range m.Docs {
		docs[k] = v
	}
	return out, docs, nil
}

// CategoryTrain implements ModelStore.
func (m *MemModel) CategoryTrain(_ context.Context, cat string, toks []string, delta int) error {
	if m.Counts[cat] == nil {
		m.Counts[cat] = map[string]int{}
	}
	for _, t := range toks {
		m.Counts[cat][t] = max(0, m.Counts[cat][t]+delta)
	}
	m.Docs[cat] = max(0, m.Docs[cat]+delta)
	return nil
}
