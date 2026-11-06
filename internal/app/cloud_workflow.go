// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Midas1989/Mailclient/internal/cloud"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
	"github.com/Midas1989/Mailclient/internal/workflow"
)

// Archive kinds of store.CloudArchiveEntry.
const (
	archiveAttachment = "attachment"
	archivePDF        = "pdf"
)

// workflowMessage loads the mail of a workflow item.
func (a *App) workflowMessage(ctx context.Context, ref workflow.ItemRef) (store.Message, []byte, error) {
	if ref.Kind != "" && ref.Kind != "mail" {
		return store.Message{}, nil, workflow.Permanent(errors.New("event does not refer to a mail"))
	}
	ms, err := a.st.MessagesByKey(ctx, ref.Key)
	if err != nil {
		return store.Message{}, nil, err
	}
	for _, m := range ms {
		if ref.AccountID == 0 || m.AccountID == ref.AccountID {
			raw, err := a.rawMessage(m)
			return m, raw, err
		}
	}
	return store.Message{}, nil, workflow.Permanent(fmt.Errorf("mail %s not found", ref.Key))
}

// workflowTarget resolves the target of a workflow action to a storage account.
func (a *App) workflowTarget(ctx context.Context, target string) (domain.Account, *cloudClient, error) {
	acc, err := a.cloudAccountByTarget(ctx, target)
	if err != nil {
		return acc, nil, workflow.Permanent(err)
	}
	c, err := a.cloudStorage(ctx, acc.ID)
	if err != nil {
		return acc, nil, workflow.Permanent(err)
	}
	return acc, c, nil
}

// workflowCloud implements workflow.CloudActions (KONZEPT 12.1 #2: "store
// attachments automatically", e.g. invoices to /Dokumente/Rechnungen/2026).
type workflowCloud struct{ a *App }

func (w workflowCloud) UploadAttachments(ctx context.Context, ref workflow.ItemRef, req workflow.UploadRequest) ([]workflow.UploadedFile, error) {
	a := w.a
	acc, c, err := a.workflowTarget(ctx, req.Target)
	if err != nil {
		return nil, err
	}
	m, raw, err := a.workflowMessage(ctx, ref)
	if err != nil {
		return nil, err
	}
	rm, err := a.render.Render(ctx, raw)
	if err != nil {
		return nil, err
	}
	if rm.Encrypted {
		// The renderer decrypts for the reading pane: a rule must not carry
		// the plaintext of encrypted attachments to a cloud (KONZEPT 8.5).
		return nil, workflow.Permanent(errors.New("attachments of PGP-encrypted mail are not uploaded by rules"))
	}
	key := m.Key()
	var out []workflow.UploadedFile
	for _, at := range rm.Attachments {
		if at.Inline && at.ContentID != "" {
			continue
		}
		if at.Size < req.MinSize {
			continue
		}
		if req.Pattern != "" {
			if ok, _ := path.Match(strings.ToLower(req.Pattern), strings.ToLower(at.Filename)); !ok {
				continue
			}
		}
		uf := workflow.UploadedFile{Name: at.Filename}
		if prev, err := a.st.GetCloudArchive(ctx, acc.ID, key, archiveAttachment, at.Filename); err == nil {
			uf.Path = prev.Path // already stored (duplicate protection)
		} else {
			_, data, err := a.render.Attachment(ctx, raw, at.Index)
			if err != nil {
				return out, err
			}
			e, err := a.uploadContent(ctx, c, req.Dir, at.Filename, bytes.NewReader(data), int64(len(data)))
			if err != nil {
				return out, cloudErr(err)
			}
			uf.Path = e.Path
			_ = a.st.PutCloudArchive(ctx, store.CloudArchiveEntry{AccountID: acc.ID, MessageKey: key, Kind: archiveAttachment,
				Name: at.Filename, Path: e.Path, CreatedAt: a.now()})
		}
		if req.ShareLink || req.ReplaceWithLink {
			exp := req.ShareExpiry
			if exp <= 0 {
				exp = send.DefaultCloudExpiry
			}
			pol := c.passwordPolicy(ctx)
			sh, err := a.createShare(ctx, shareRequest{accountID: acc.ID, path: uf.Path, name: at.Filename, size: at.Size,
				opts: cloud.ShareOptions{Expires: a.now().Add(exp)}, genPass: pol.Enforced, source: store.CloudSourceWorkflow, messageID: m.ID})
			if err != nil {
				return out, err
			}
			uf.URL = sh.URL
		}
		out = append(out, uf)
	}
	return out, nil
}

// workflowPDF implements workflow.PDFActions: the PDF is produced by the
// installed PDFRenderer and stored locally (desktop: below
// <data dir>/archive) or in a cloud account.
type workflowPDF struct{ a *App }

func (w workflowPDF) ArchivePDF(ctx context.Context, ref workflow.ItemRef, req workflow.PDFRequest) (string, error) {
	a := w.a
	a.cloud.mu.Lock()
	render := a.cloud.pdf
	a.cloud.mu.Unlock()
	if render == nil {
		return "", workflow.Permanent(errors.New("no PDF renderer is configured"))
	}
	local := strings.EqualFold(req.Target, "local")
	var acc domain.Account
	var c *cloudClient
	var err error
	if !local {
		if acc, c, err = a.workflowTarget(ctx, req.Target); err != nil {
			return "", err
		}
	}
	m, raw, err := a.workflowMessage(ctx, ref)
	if err != nil {
		return "", err
	}
	key := m.Key()
	if req.SkipDuplicates && !local {
		if prev, err := a.st.GetCloudArchive(ctx, acc.ID, key, archivePDF, ""); err == nil {
			return prev.Path, nil
		}
	}
	pdf, err := render(ctx, raw, PDFRenderRequest{EmbedOriginal: req.EmbedOriginal, IncludeAttachments: req.IncludeAttachments})
	if err != nil {
		return "", err
	}
	if local {
		return a.storeLocalPDF(req.Path, pdf)
	}
	dir, name := path.Split(cloud.Clean(req.Path))
	e, err := a.uploadContent(ctx, c, dir, name, bytes.NewReader(pdf), int64(len(pdf)))
	if err != nil {
		return "", cloudErr(err)
	}
	_ = a.st.PutCloudArchive(ctx, store.CloudArchiveEntry{AccountID: acc.ID, MessageKey: key, Kind: archivePDF, Path: e.Path, CreatedAt: a.now()})
	return e.Path, nil
}

// storeLocalPDF writes below <data dir>/archive (desktop only: in server
// mode workflows must not write to the server's file system).
func (a *App) storeLocalPDF(p string, pdf []byte) (string, error) {
	if a.opts.Mode == ModeServer {
		return "", workflow.Permanent(errors.New("target local is not available in server mode"))
	}
	base, err := a.userDataDir("archive")
	if err != nil {
		return "", workflow.Permanent(err)
	}
	rel := filepath.FromSlash(strings.TrimPrefix(cloud.Clean(p), "/"))
	full := filepath.Join(base, rel)
	if !strings.HasPrefix(full, base+string(filepath.Separator)) {
		return "", workflow.Permanent(errors.New("invalid path"))
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(full); err == nil {
		ext := filepath.Ext(full)
		for i := 2; ; i++ {
			cand := fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(full, ext), i, ext)
			if _, err := os.Stat(cand); os.IsNotExist(err) {
				full = cand
				break
			}
		}
	}
	return full, os.WriteFile(full, pdf, 0o600)
}

var (
	_ workflow.CloudActions = workflowCloud{}
	_ workflow.PDFActions   = workflowPDF{}
)
