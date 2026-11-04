// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/merge"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
)

// MergeTable is the result of parsing a recipient table.
type MergeTable struct {
	Recipients []merge.Recipient `json:"recipients"`
	Columns    []string          `json:"columns"`
}

// MergeJobInput creates a mail merge job.
type MergeJobInput struct {
	IdentityID  int64             `json:"identityId"`
	Template    merge.Template    `json:"template"`
	Recipients  []merge.Recipient `json:"recipients"`
	PerMinute   int               `json:"perMinute,omitempty"`
	Unsubscribe string            `json:"unsubscribe,omitempty"`
	Encrypt     bool              `json:"encrypt,omitempty"`
	// Attachments are upload tokens; they are shared as cloud links (one
	// upload for all recipients) when CloudAccount is set, else attached.
	Attachments  []string `json:"attachments,omitempty"`
	CloudAccount string   `json:"cloudAccount,omitempty"`
}

// MergeJobView is a job with its counts.
type MergeJobView struct {
	merge.Job
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
}

func jobView(j *merge.Job) MergeJobView {
	s, f, p := j.Counts()
	return MergeJobView{Job: *j, Sent: s, Failed: f, Pending: p}
}

// ParseMergeTable reads recipients from a CSV/XLSX file (base64).
func (a *App) ParseMergeTable(_ context.Context, data string) (MergeTable, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil {
		return MergeTable{}, invalid("data must be base64")
	}
	rs, cols, err := merge.ParseTable(raw)
	if err != nil {
		return MergeTable{}, invalid("%v", err)
	}
	return MergeTable{Recipients: rs, Columns: cols}, nil
}

// MergeRecipientsFromGroup returns the contacts of a contact group as merge
// recipients (one per contact, first e-mail address).
func (a *App) MergeRecipientsFromGroup(ctx context.Context, group string) (MergeTable, error) {
	cs, err := a.ListContacts(ctx, ContactQuery{Group: group})
	if err != nil {
		return MergeTable{}, err
	}
	out := MergeTable{Columns: []string{"vorname", "nachname", "name", "email", "firma"}}
	for _, c := range cs {
		if len(c.Emails) == 0 {
			continue
		}
		out.Recipients = append(out.Recipients, merge.Recipient{Email: c.Emails[0].Value, Name: c.DisplayName,
			Fields: map[string]string{"vorname": c.N.Given, "nachname": c.N.Family, "name": c.DisplayName, "email": c.Emails[0].Value, "firma": c.Org}})
	}
	return out, nil
}

// PreviewMerge renders the template for the first recipients.
func (a *App) PreviewMerge(_ context.Context, in MergeJobInput, n int) ([]merge.Rendered, error) {
	if n <= 0 || n > 50 {
		n = 10
	}
	return merge.Preview(merge.Job{Template: in.Template, Recipients: in.Recipients}, n), nil
}

func newJobID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (a *App) saveJob(ctx context.Context, j *merge.Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return a.st.PutMergeJob(ctx, store.MergeJobRow{ID: j.ID, State: j.State, Data: string(b), CreatedAt: j.Created})
}

// StartMerge creates and starts a mail merge job.
func (a *App) StartMerge(ctx context.Context, in MergeJobInput) (MergeJobView, error) {
	if in.IdentityID == 0 {
		return MergeJobView{}, invalid("identity required")
	}
	if in.PerMinute <= 0 {
		in.PerMinute = a.comfortSettings().Merge.PerMinute
	}
	j := &merge.Job{ID: newJobID(), IdentityID: in.IdentityID, Template: in.Template, Recipients: in.Recipients, PerMinute: in.PerMinute,
		Unsubscribe: in.Unsubscribe, Encrypt: in.Encrypt, Created: a.now(), State: "draft"}
	for _, t := range in.Attachments {
		j.CloudFiles = append(j.CloudFiles, t)
	}
	if in.CloudAccount != "" {
		j.CloudFiles = append(j.CloudFiles, "cloud:"+in.CloudAccount)
	}
	if err := merge.Validate(j); err != nil {
		return MergeJobView{}, invalid("%v", err)
	}
	if err := a.saveJob(ctx, j); err != nil {
		return MergeJobView{}, err
	}
	a.runJob(j, false)
	return jobView(j), nil
}

// runJob runs a job in the background.
func (a *App) runJob(j *merge.Job, retry bool) {
	c := a.comfort
	ctx, cancel := context.WithCancel(context.Background())
	c.jobsMu.Lock()
	if old, ok := c.jobs[j.ID]; ok {
		old()
	}
	c.jobs[j.ID] = cancel
	c.jobsMu.Unlock()
	r := &merge.Runner{Sender: c.runner.Sender, Sleep: c.runner.Sleep, Now: c.runner.Now}
	r.OnProgress = func(j *merge.Job) {
		_ = a.saveJob(context.Background(), j)
		a.hub.Publish(Event{Type: EventMergeProgress, ItemID: j.ID, State: j.State})
	}
	go func() {
		defer func() {
			c.jobsMu.Lock()
			delete(c.jobs, j.ID)
			c.jobsMu.Unlock()
		}()
		if err := r.Run(ctx, j, retry); err != nil && ctx.Err() == nil {
			a.log.Warn("comfort: mail merge stopped", "job", j.ID, "err", err)
		}
	}()
}

// EventMergeProgress is published after every mail of a merge job.
const EventMergeProgress = "merge.progress"

// ListMergeJobs returns the jobs.
func (a *App) ListMergeJobs(ctx context.Context) ([]MergeJobView, error) {
	rows, err := a.st.ListMergeJobs(ctx, 50)
	if err != nil {
		return nil, err
	}
	out := []MergeJobView{}
	for _, r := range rows {
		var j merge.Job
		if json.Unmarshal([]byte(r.Data), &j) == nil {
			out = append(out, jobView(&j))
		}
	}
	return out, nil
}

// GetMergeJob returns one job.
func (a *App) GetMergeJob(ctx context.Context, id string) (MergeJobView, error) {
	r, err := a.st.GetMergeJob(ctx, id)
	if err != nil {
		return MergeJobView{}, err
	}
	var j merge.Job
	if err := json.Unmarshal([]byte(r.Data), &j); err != nil {
		return MergeJobView{}, err
	}
	return jobView(&j), nil
}

// RetryMergeJob resends the failed entries (and resumes pending ones).
func (a *App) RetryMergeJob(ctx context.Context, id string) (MergeJobView, error) {
	v, err := a.GetMergeJob(ctx, id)
	if err != nil {
		return v, err
	}
	j := v.Job
	a.runJob(&j, true)
	return jobView(&j), nil
}

// CancelMergeJob stops a running job.
func (a *App) CancelMergeJob(ctx context.Context, id string) error {
	a.comfort.jobsMu.Lock()
	cancel, ok := a.comfort.jobs[id]
	a.comfort.jobsMu.Unlock()
	if ok {
		cancel()
		return nil
	}
	v, err := a.GetMergeJob(ctx, id)
	if err != nil {
		return err
	}
	j := v.Job
	if j.State != "done" {
		j.State = "cancelled"
		return a.saveJob(ctx, &j)
	}
	return nil
}

// mergeSender sends merge mails as individual messages through the send
// pipeline (with per-recipient OpenPGP/S/MIME when keys exist).
type mergeSender struct{ a *App }

func (m mergeSender) SendMerge(ctx context.Context, msg merge.Message) error {
	a := m.a
	ms, ok := a.opts.Sender.(*MailSender)
	if !ok {
		return Errorf(501, CodeSendNotAvailable, "sending is not configured")
	}
	r := msg.Rendered
	d := compose.Draft{To: []domain.Address{{Name: r.Recipient.Name, Email: r.Recipient.Email}}, Subject: r.Subject,
		TextBody: r.Text, HTMLBody: r.HTML, KeepSubject: true}
	if msg.Unsubscribe != "" {
		d.Headers = append(d.Headers, compose.Header{Name: "List-Unsubscribe", Value: "<" + msg.Unsubscribe + ">"})
		if strings.HasPrefix(strings.ToLower(msg.Unsubscribe), "https:") {
			d.Headers = append(d.Headers, compose.Header{Name: "List-Unsubscribe-Post", Value: "List-Unsubscribe=One-Click"})
		}
	}
	d.Headers = append(d.Headers, compose.Header{Name: "Precedence", Value: "bulk"})
	req := send.Request{IdentityID: msg.IdentityID, Source: "merge"}
	if msg.Encrypt {
		req.PGP.Encrypt, req.SMIME.Encrypt = send.PGPAuto, send.PGPAuto
	} else {
		req.PGP.Encrypt, req.SMIME.Encrypt = send.PGPOff, send.PGPOff
	}
	cloudAcc := ""
	for _, t := range msg.CloudFiles {
		if acc, ok := strings.CutPrefix(t, "cloud:"); ok {
			cloudAcc = acc
		}
	}
	for _, t := range msg.CloudFiles {
		if strings.HasPrefix(t, "cloud:") {
			continue
		}
		u, err := a.st.GetUpload(ctx, t)
		if err != nil {
			return invalid("unknown upload token")
		}
		ca := compose.Attachment{Filename: u.Filename, ContentType: u.MIMEType, BlobRef: u.BlobHash}
		if cloudAcc != "" {
			acc, err := a.deliveryAccount(ctx, cloudAcc)
			if err != nil {
				return err
			}
			req.Cloud.Files = append(req.Cloud.Files, ca)
			req.Cloud.FileAccounts = append(req.Cloud.FileAccounts, acc)
		} else {
			d.Attachments = append(d.Attachments, ca)
		}
	}
	req.Draft = d
	_, err := ms.Service.Send(ctx, req)
	return err
}
