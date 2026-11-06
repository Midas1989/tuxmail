// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/mail"
	"net/textproto"
	"strings"

	"github.com/Midas1989/Mailclient/internal/categorize"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/einvoice"
	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/spam"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/unsubscribe"
)

// spamStore adapts *store.Store to spam.TokenStore.
type spamStore struct{ st *store.Store }

func (s spamStore) SpamTotals(ctx context.Context) (int, int, error) { return s.st.SpamTotals(ctx) }
func (s spamStore) SpamTrain(ctx context.Context, h []int64, isSpam bool, d int) error {
	return s.st.SpamTrain(ctx, h, isSpam, d)
}
func (s spamStore) SpamCounts(ctx context.Context, h []int64) (map[int64]spam.Count, error) {
	m, err := s.st.SpamTokenCounts(ctx, h)
	out := make(map[int64]spam.Count, len(m))
	for k, v := range m {
		out[k] = spam.Count{Spam: v.Spam, Ham: v.Ham}
	}
	return out, err
}

// SpamInfo is the spam part of MessageInsights.
type SpamInfo struct {
	Score   int      `json:"score"` // 0..100
	Level   string   `json:"level"` // ham | unsure | spam
	Server  bool     `json:"server"`
	Trained bool     `json:"trained"`
	Reasons []string `json:"reasons,omitempty"`
}

// MessageInsights is everything the reading pane shows about a mail
// (GET /api/comfort/messages/{id}/insights).
type MessageInsights struct {
	MessageID      int64              `json:"messageId"`
	Spam           SpamInfo           `json:"spam"`
	Category       string             `json:"category,omitempty"`
	CategorySource string             `json:"categorySource,omitempty"`
	Unsubscribe    *unsubscribe.Info  `json:"unsubscribe,omitempty"`
	Sender         string             `json:"sender,omitempty"`
	SenderName     string             `json:"senderName,omitempty"`
	Unsubscribed   bool               `json:"unsubscribed,omitempty"`
	Invoice        *einvoice.Invoice  `json:"invoice,omitempty"`
	SMIME          *parse.SMIMEInfo   `json:"smime,omitempty"`
	PGP            *PGPMessageStatus  `json:"pgp,omitempty"`
	Reasons        map[string]float64 `json:"categoryScores,omitempty"`
}

type analysis struct {
	insight store.Insight
	verdict spam.Verdict
	cat     categorize.Result
	unsub   unsubscribe.Info
	invoice *einvoice.Invoice
	smime   parse.SMIMEInfo
	msg     *parse.Message
}

// headersOf converts the parsed header to a plain map.
func headersOf(m *parse.Message) map[string][]string {
	out := map[string][]string{}
	f := m.Headers.Fields()
	for f.Next() {
		k := textproto.CanonicalMIMEHeaderKey(f.Key())
		out[k] = append(out[k], f.Value())
	}
	return out
}

// fromEmail returns the first From address of a parsed message.
func fromEmail(m *parse.Message) string {
	if len(m.Header.From) > 0 {
		return strings.ToLower(m.Header.From[0].Email)
	}
	return ""
}

// analyze runs the spam filter, categorizer, e-invoice and unsubscribe
// detection on a raw message.
func (a *App) analyze(ctx context.Context, msg store.Message, raw []byte) (*analysis, error) {
	opts := parse.Options{SMIME: smimeReader{a: a, collect: true}}
	pm, err := parse.ParseContext(ctx, raw, opts)
	if err != nil {
		return nil, err
	}
	s := a.comfortSettings()
	an := &analysis{msg: pm, smime: pm.SMIME}
	hdr := headersOf(pm)
	from := strings.ToLower(fromEmail(pm))
	sm := spam.Message{From: from, Subject: pm.Header.Subject, Text: pm.Text, HTML: pm.HTML, Headers: hdr}
	if s.Spam.Enabled {
		c := *a.comfort.spamC
		c.Thresholds = spam.Thresholds{Unsure: float64(s.Spam.Unsure) / 100, Spam: float64(s.Spam.Spam) / 100}
		if an.verdict, err = c.Classify(ctx, sm); err != nil {
			return nil, err
		}
	} else {
		an.verdict = spam.Verdict{Level: spam.LevelHam}
	}
	if an.verdict.Level != spam.LevelSpam {
		a.collectAutocrypt(ctx, pm) // Autocrypt: spam must not change a peer's key
	}
	var atts []einvoice.Attachment
	cm := categorize.Mail{From: from, Subject: pm.Header.Subject, Text: pm.Text, Headers: hdr, HasCalendar: len(pm.CalendarParts) > 0}
	for _, at := range pm.Attachments {
		cm.AttachmentNames = append(cm.AttachmentNames, at.Filename)
		cm.AttachmentTypes = append(cm.AttachmentTypes, at.MIMEType)
		lt := strings.ToLower(at.Filename + " " + at.MIMEType)
		if strings.Contains(lt, "pdf") || strings.Contains(lt, "xml") {
			atts = append(atts, einvoice.Attachment{Name: at.Filename, MIME: at.MIMEType, Data: at.Data})
		}
	}
	if inv, err := einvoice.Detect(atts); err == nil {
		an.invoice = inv
		cm.IsInvoice = true
	}
	if s.Categories.Enabled {
		cm.KnownContact = a.isKnownSender(ctx, from)
		cc := *a.comfort.catC
		if s.Categories.AIRefine {
			cc.Refiner = aiRefiner{a: a}
		}
		if an.cat, err = cc.Classify(ctx, cm); err != nil {
			return nil, err
		}
	}
	h := pm.Headers
	an.unsub = unsubscribe.Parse(h.Get("List-Unsubscribe"), h.Get("List-Unsubscribe-Post"), h.Get("List-Id"))
	in := store.Insight{MessageID: msg.ID, SpamScore: math.Round(an.verdict.Score*1000) / 10, SpamLevel: string(an.verdict.Level),
		Category: an.cat.Category, CategorySource: an.cat.Source, CategoryConf: an.cat.Confidence, Sender: from, ListID: an.unsub.ListID}
	if !an.verdict.Trained && !an.verdict.Server.Present {
		in.SpamScore = 0
	}
	if an.unsub.Available() {
		b, _ := json.Marshal(an.unsub)
		in.Unsubscribe = string(b)
	}
	if an.invoice != nil {
		b, _ := json.Marshal(an.invoice)
		in.Invoice = string(b)
	}
	if an.smime.Present {
		b, _ := json.Marshal(an.smime)
		in.SMIME = string(b)
	}
	an.insight = in
	return an, nil
}

// isKnownSender reports whether the user has written to the address.
func (a *App) isKnownSender(ctx context.Context, email string) bool {
	if email == "" {
		return false
	}
	var n int
	err := a.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM message_addresses ma JOIN messages m ON m.id = ma.message_id
		JOIN message_locations l ON l.message_id = m.id JOIN folders f ON f.id = l.folder_id
		WHERE f.role = 'sent' AND ma.role IN ('to', 'cc') AND lower(ma.email) = ? LIMIT 1`, email).Scan(&n)
	return err == nil && n > 0
}

// processMessage analyzes and stores the insight of a message.
func (a *App) processMessage(ctx context.Context, msg store.Message, raw []byte) (*analysis, error) {
	an, err := a.analyze(ctx, msg, raw)
	if err != nil {
		return nil, err
	}
	if prev, err := a.st.GetInsight(ctx, msg.ID); err == nil && prev.CategorySource == "user" {
		an.insight.Category, an.insight.CategorySource = prev.Category, prev.CategorySource
	}
	return an, a.st.PutInsight(ctx, an.insight)
}

// comfortProcessor implements the sync engine's MailProcessor.
type comfortProcessor struct{ a *App }

func (p comfortProcessor) ProcessNewMail(ctx context.Context, msg store.Message, raw []byte, role domain.FolderRole) {
	if role == domain.RoleSent || role == domain.RoleDrafts {
		return
	}
	if _, err := p.a.processMessage(ctx, msg, raw); err != nil {
		p.a.log.Debug("comfort: analysis failed", "message", msg.ID, "err", err)
	}
}

func (p comfortProcessor) EnrichMailEvent(ctx context.Context, msg store.Message, m *events.Mail) {
	p.a.enrichMail(ctx, msg.ID, m)
}

// enrichMail copies the stored insight into a mail event.
func (a *App) enrichMail(ctx context.Context, messageID int64, m *events.Mail) {
	in, err := a.st.GetInsight(ctx, messageID)
	if err != nil {
		return
	}
	if m.Category == "" {
		m.Category = in.Category
	}
	m.SpamScore, m.IsSpam = in.SpamScore, in.SpamLevel == string(spam.LevelSpam)
	if in.Invoice != "" {
		var inv einvoice.Invoice
		if json.Unmarshal([]byte(in.Invoice), &inv) == nil {
			m.Invoice = &events.Invoice{Format: inv.Format, Number: inv.Number, Issued: inv.Issued, Due: inv.Due, Amount: inv.AmountFloat(),
				Currency: inv.Currency, Seller: inv.Seller, IBAN: inv.IBAN}
			m.IsInvoice = true
		}
	}
	if in.Category == categorize.Invoice {
		m.IsInvoice = true
	}
}

// backfillInsights analyzes mails stored before the comfort features ran
// (or whose body arrived later).
func (a *App) backfillInsights(ctx context.Context, n int) {
	ids, err := a.st.MessagesWithoutInsight(ctx, n)
	if err != nil {
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		m, err := a.st.GetMessage(ctx, id)
		if err != nil {
			continue
		}
		raw, err := a.rawMessage(m)
		if err != nil {
			_ = a.st.PutInsight(ctx, store.Insight{MessageID: id}) // do not retry forever
			continue
		}
		if _, err := a.processMessage(ctx, m, raw); err != nil {
			_ = a.st.PutInsight(ctx, store.Insight{MessageID: id})
		}
	}
}

// MessageInsights returns (computing if needed) the analysis of a mail.
func (a *App) MessageInsights(ctx context.Context, id int64) (MessageInsights, error) {
	m, err := a.st.GetMessage(ctx, id)
	if err != nil {
		return MessageInsights{}, err
	}
	in, err := a.st.GetInsight(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && in.SpamLevel == "" && in.Category == "") {
		raw, rerr := a.rawMessage(m)
		if rerr == nil {
			if an, perr := a.processMessage(ctx, m, raw); perr == nil {
				in, err = an.insight, nil
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			return MessageInsights{MessageID: id, Spam: SpamInfo{Level: "ham"}, PGP: a.messagePGPStatus(ctx, m)}, nil
		}
	}
	if err != nil {
		return MessageInsights{}, err
	}
	out := MessageInsights{MessageID: id, Category: in.Category, CategorySource: in.CategorySource, Sender: in.Sender,
		Spam: SpamInfo{Score: int(math.Round(in.SpamScore)), Level: in.SpamLevel}}
	if out.Spam.Level == "" {
		out.Spam.Level = "ham"
	}
	if in.Unsubscribe != "" {
		var u unsubscribe.Info
		if json.Unmarshal([]byte(in.Unsubscribe), &u) == nil {
			out.Unsubscribe = &u
			if sub, err := a.subscriptionState(ctx, subscriptionKey(in)); err == nil && sub.unsubscribedAt > 0 {
				out.Unsubscribed = true
			}
		}
	}
	if in.Invoice != "" {
		var inv einvoice.Invoice
		if json.Unmarshal([]byte(in.Invoice), &inv) == nil {
			out.Invoice = &inv
		}
	}
	if in.SMIME != "" {
		var si parse.SMIMEInfo
		if json.Unmarshal([]byte(in.SMIME), &si) == nil {
			out.SMIME = &si
		}
	}
	out.PGP = a.messagePGPStatus(ctx, m)
	if addrs, err := a.st.MessageAddresses(ctx, id); err == nil {
		for _, ad := range addrs {
			if ad.Role == store.RoleFrom {
				out.SenderName = ad.Name
				break
			}
		}
	}
	return out, nil
}

// ==== spam training ===============================================================

// MarkSpamInput is the body of POST /api/comfort/spam.
type MarkSpamInput struct {
	IDs  []int64 `json:"ids"`
	Spam bool    `json:"spam"`
	// Move moves the mails to the junk folder (spam) or back to the inbox.
	Move bool `json:"move"`
}

// MarkSpam trains the filter with the user's verdict ("Spam" / "Kein
// Spam"), sets the IMAP $Junk/$NotJunk keywords and optionally moves the mails.
func (a *App) MarkSpam(ctx context.Context, in MarkSpamInput) error {
	if len(in.IDs) == 0 {
		return nil
	}
	if err := checkBulk(len(in.IDs)); err != nil {
		return err
	}
	s := a.comfortSettings()
	for _, id := range in.IDs {
		m, err := a.st.GetMessage(ctx, id)
		if err != nil {
			return err
		}
		if err := a.trainSpam(ctx, m, in.Spam); err != nil {
			return err
		}
	}
	if s.Spam.ServerLearn {
		add, rem := []domain.Flag{"$NotJunk"}, []domain.Flag{"$Junk"}
		if in.Spam {
			add, rem = rem, add
		}
		if err := a.sync.SetFlags(ctx, in.IDs, add, rem); err != nil {
			a.log.Info("comfort: setting junk keywords failed", "err", err)
		}
	}
	if in.Move {
		role := domain.RoleInbox
		if in.Spam {
			role = domain.RoleJunk
		}
		byAcc := map[int64][]int64{}
		for _, id := range in.IDs {
			if m, err := a.st.GetMessage(ctx, id); err == nil {
				byAcc[m.AccountID] = append(byAcc[m.AccountID], id)
			}
		}
		for acc, ids := range byAcc {
			f, err := a.st.FolderByRole(ctx, acc, role)
			if err != nil {
				continue
			}
			if err := a.Move(ctx, ids, f.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// trainSpam trains one message, correcting an earlier opposite training.
func (a *App) trainSpam(ctx context.Context, m store.Message, isSpam bool) error {
	key := m.Key()
	was, trained, err := a.st.SpamTrainedAs(ctx, key)
	if err != nil {
		return err
	}
	if trained && was == isSpam {
		return nil
	}
	raw, err := a.rawMessage(m)
	if err != nil {
		return err
	}
	pm, err := parse.Parse(raw, parse.Options{SkipAttachmentData: true})
	if err != nil {
		return err
	}
	toks := spam.Tokenize(spam.Message{From: fromEmail(pm), Subject: pm.Header.Subject, Text: pm.Text, HTML: pm.HTML, Headers: headersOf(pm)})
	c := a.comfort.spamC
	if trained {
		if err := c.Untrain(ctx, toks, was); err != nil {
			return err
		}
	}
	if err := c.Train(ctx, toks, isSpam); err != nil {
		return err
	}
	if err := a.st.SetSpamTrained(ctx, key, isSpam); err != nil {
		return err
	}
	in, err := a.st.GetInsight(ctx, m.ID)
	if err != nil {
		in = store.Insight{MessageID: m.ID}
	}
	if isSpam {
		in.SpamLevel, in.SpamScore = string(spam.LevelSpam), math.Max(in.SpamScore, 99)
	} else {
		in.SpamLevel, in.SpamScore = string(spam.LevelHam), math.Min(in.SpamScore, 1)
	}
	return a.st.PutInsight(ctx, in)
}

// comfortEvent reacts to mail events: automatic junk moves and training
// when the user moves mail into or out of the junk folder.
func (a *App) comfortEvent(ctx context.Context, ev events.Event) {
	m := ev.Mail
	if m == nil {
		return
	}
	s := a.comfortSettings()
	switch ev.Type {
	case events.MailReceived:
		if !s.Spam.Enabled || !s.Spam.MoveToJunk || !m.IsSpam || m.FolderRole != string(domain.RoleInbox) {
			return
		}
		ms, _ := a.st.MessagesByKey(ctx, m.Key)
		for _, msg := range ms {
			if msg.AccountID != m.AccountID {
				continue
			}
			if f, err := a.st.FolderByRole(ctx, msg.AccountID, domain.RoleJunk); err == nil {
				if err := a.Move(ctx, []int64{msg.ID}, f.ID); err != nil {
					a.log.Info("comfort: moving spam failed", "err", err)
				}
			}
		}
	case events.MailMoved:
		if ev.Source != "user" || !s.Spam.Enabled {
			return
		}
		old, err := a.st.FolderByRemoteID(ctx, m.AccountID, m.OldFolder)
		if err != nil {
			return
		}
		intoJunk := m.FolderRole == string(domain.RoleJunk) && old.Role != domain.RoleJunk
		outOfJunk := old.Role == domain.RoleJunk && m.FolderRole != string(domain.RoleJunk) && m.FolderRole != string(domain.RoleTrash)
		if !intoJunk && !outOfJunk {
			return
		}
		ms, _ := a.st.MessagesByKey(ctx, m.Key)
		for _, msg := range ms {
			if msg.AccountID == m.AccountID {
				if err := a.trainSpam(ctx, msg, intoJunk); err != nil {
					a.log.Info("comfort: spam training failed", "err", err)
				}
			}
		}
	}
}

// ==== categories ====================================================================

// SetCategoryInput is the body of POST /api/comfort/category.
type SetCategoryInput struct {
	IDs      []int64 `json:"ids"`
	Category string  `json:"category"`
}

// SetMessageCategory moves mails to another category and retrains the model.
func (a *App) SetMessageCategory(ctx context.Context, in SetCategoryInput) error {
	cat := categorize.Normalize(in.Category)
	if cat == "" {
		return invalid("unknown category %q", in.Category)
	}
	for _, id := range in.IDs {
		m, err := a.st.GetMessage(ctx, id)
		if err != nil {
			return err
		}
		prev := ""
		if ins, err := a.st.GetInsight(ctx, id); err == nil {
			prev = ins.Category
		}
		if raw, err := a.rawMessage(m); err == nil {
			if pm, err := parse.Parse(raw, parse.Options{SkipAttachmentData: true}); err == nil {
				cm := categorize.Mail{From: fromEmail(pm), Subject: pm.Header.Subject, Text: pm.Text, Headers: headersOf(pm),
					HasCalendar: len(pm.CalendarParts) > 0}
				if err := a.comfort.catC.Learn(ctx, cm, prev, cat); err != nil {
					return err
				}
			}
		}
		if err := a.st.SetMessageCategory(ctx, id, cat, "user"); err != nil {
			return err
		}
		a.hub.Publish(Event{Type: EventMessageUpdated, MessageID: id, ConversationID: m.ConversationID})
	}
	return nil
}

// CategoryInfo is a category with its counts.
type CategoryInfo struct {
	ID     string `json:"id"`
	Total  int    `json:"total"`
	Unread int    `json:"unread"`
}

// ListCategories returns all categories with counts (role "" = all folders).
func (a *App) ListCategories(ctx context.Context, role string) ([]CategoryInfo, error) {
	counts, err := a.st.CategoryTotals(ctx, role)
	if err != nil {
		return nil, err
	}
	by := map[string]store.CategoryCount{}
	for _, c := range counts {
		by[c.Category] = c
	}
	out := make([]CategoryInfo, 0, len(categorize.All))
	for _, id := range categorize.All {
		out = append(out, CategoryInfo{ID: id, Total: by[id].Total, Unread: by[id].Unread})
	}
	return out, nil
}

// CategoryMessages lists the mails of a category.
func (a *App) CategoryMessages(ctx context.Context, category, role string, limit int) ([]MessageSummary, error) {
	cat := categorize.Normalize(category)
	if cat == "" {
		return nil, invalid("unknown category %q", category)
	}
	ids, err := a.st.MessagesInCategory(ctx, cat, role, limit)
	if err != nil {
		return nil, err
	}
	msgs, err := a.st.GetMessages(ctx, ids)
	if err != nil {
		return nil, err
	}
	return a.summaries(ctx, msgs)
}

// MessageCategories returns the categories of the given messages (for
// list filtering in the UI): message id -> category.
func (a *App) MessageCategories(ctx context.Context, ids []int64) (map[int64]string, error) {
	if err := checkBulk(len(ids)); err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, id := range ids {
		if in, err := a.st.GetInsight(ctx, id); err == nil && in.Category != "" {
			out[id] = in.Category
		}
	}
	return out, nil
}

func addressName(s string) string {
	if a, err := mail.ParseAddress(s); err == nil && a.Name != "" {
		return a.Name
	}
	return s
}
