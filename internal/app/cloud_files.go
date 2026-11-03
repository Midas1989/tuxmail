// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/Midas1989/Mailclient/internal/cloud"
	"github.com/Midas1989/Mailclient/internal/cloud/vaultwarden"
	"github.com/Midas1989/Mailclient/internal/send"
	"github.com/Midas1989/Mailclient/internal/store"
)

// maxCloudTransfer limits files moved between the cloud and TuxMail in one
// request (downloads into uploads, Vaultwarden Sends).
const maxCloudTransfer = maxUpload

func toCloudEntry(e cloud.Entry) CloudEntry {
	out := CloudEntry{Name: e.Name, Path: e.Path, Dir: e.Dir, Size: e.Size, ContentType: e.ContentType, Modified: rfc3339(e.Modified)}
	if !out.Dir && out.ContentType == "" {
		out.ContentType = mime.TypeByExtension(path.Ext(e.Name))
	}
	return out
}

// ListCloudFiles lists a directory (directories first, then by name).
func (a *App) ListCloudFiles(ctx context.Context, accountID int64, dir string) (CloudListing, error) {
	c, err := a.cloudStorage(ctx, accountID)
	if err != nil {
		return CloudListing{}, err
	}
	dir = cloud.Clean(dir)
	es, err := c.storage.List(ctx, dir)
	if err != nil {
		return CloudListing{}, cloudErr(err)
	}
	out := CloudListing{AccountID: accountID, Path: dir, Entries: make([]CloudEntry, 0, len(es))}
	for _, e := range es {
		out.Entries = append(out.Entries, toCloudEntry(e))
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		x, y := out.Entries[i], out.Entries[j]
		if x.Dir != y.Dir {
			return x.Dir
		}
		return strings.ToLower(x.Name) < strings.ToLower(y.Name)
	})
	return out, nil
}

// CloudQuota returns the storage quota of an account.
func (a *App) CloudQuota(ctx context.Context, accountID int64) (CloudQuota, error) {
	c, err := a.cloudStorage(ctx, accountID)
	if err != nil {
		return CloudQuota{}, err
	}
	q, err := c.storage.Quota(ctx)
	return CloudQuota{Used: q.Used, Available: q.Available}, cloudErr(err)
}

// CreateCloudFolder creates a directory (and missing parents).
func (a *App) CreateCloudFolder(ctx context.Context, accountID int64, p string) (CloudEntry, error) {
	c, err := a.cloudStorage(ctx, accountID)
	if err != nil {
		return CloudEntry{}, err
	}
	if cloud.Clean(p) == "/" {
		return CloudEntry{}, invalid("folder name required")
	}
	if err := cloud.MkdirAll(ctx, c.storage, p); err != nil {
		return CloudEntry{}, cloudErr(err)
	}
	e, err := c.storage.Stat(ctx, p)
	return toCloudEntry(e), cloudErr(err)
}

// MoveCloudFile renames or moves a file or directory.
func (a *App) MoveCloudFile(ctx context.Context, accountID int64, from, to string) error {
	c, err := a.cloudStorage(ctx, accountID)
	if err != nil {
		return err
	}
	if cloud.Clean(from) == "/" || cloud.Clean(to) == "/" {
		return invalid("invalid path")
	}
	return cloudErr(c.storage.Move(ctx, from, to))
}

// DeleteCloudFile deletes a file or directory.
func (a *App) DeleteCloudFile(ctx context.Context, accountID int64, p string) error {
	c, err := a.cloudStorage(ctx, accountID)
	if err != nil {
		return err
	}
	return cloudErr(c.storage.Delete(ctx, p))
}

// OpenCloudFile opens a file for download.
func (a *App) OpenCloudFile(ctx context.Context, accountID int64, p string) (io.ReadCloser, CloudEntry, error) {
	c, err := a.cloudStorage(ctx, accountID)
	if err != nil {
		return nil, CloudEntry{}, err
	}
	rc, e, err := c.storage.Download(ctx, p)
	if err != nil {
		return nil, CloudEntry{}, cloudErr(err)
	}
	return rc, toCloudEntry(e), nil
}

// DownloadCloudFile returns the content of a file (desktop IPC).
func (a *App) DownloadCloudFile(ctx context.Context, accountID int64, p string) (AttachmentContent, error) {
	rc, e, err := a.OpenCloudFile(ctx, accountID, p)
	if err != nil {
		return AttachmentContent{}, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxCloudTransfer+1))
	if err != nil {
		return AttachmentContent{}, cloudErr(err)
	}
	if len(data) > maxCloudTransfer {
		return AttachmentContent{}, Errorf(http.StatusRequestEntityTooLarge, "too_large", "file is larger than %d MB", maxCloudTransfer>>20)
	}
	ct := e.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	return AttachmentContent{Filename: e.Name, MIMEType: ct, Data: data}, nil
}

// uploadContent stores r below dir under a unique name.
func (a *App) uploadContent(ctx context.Context, c *cloudClient, dir, name string, r io.Reader, size int64) (cloud.Entry, error) {
	name = cloud.SafeName(name)
	if name == "" {
		name = "file"
	}
	dir, err := c.resolve(ctx, dir)
	if err != nil {
		return cloud.Entry{}, err
	}
	if err := cloud.MkdirAll(ctx, c.storage, dir); err != nil {
		return cloud.Entry{}, err
	}
	p, err := cloud.UniquePath(ctx, c.storage, cloud.Join(dir, name))
	if err != nil {
		return cloud.Entry{}, err
	}
	return c.storage.Upload(ctx, p, r, size)
}

// openUpload opens an upload (blob store).
func (a *App) openUpload(ctx context.Context, token string) (store.Upload, io.ReadCloser, error) {
	u, err := a.st.GetUpload(ctx, token)
	if err != nil {
		return u, nil, invalid("unknown upload token")
	}
	if a.blobs == nil {
		return u, nil, notSupported("uploads")
	}
	rc, err := a.blobs.Open(u.BlobHash)
	return u, rc, err
}

// UploadCloudFile copies an upload (Api.uploadAttachment) into dir.
func (a *App) UploadCloudFile(ctx context.Context, accountID int64, dir, uploadToken string) (CloudEntry, error) {
	c, err := a.cloudStorage(ctx, accountID)
	if err != nil {
		return CloudEntry{}, err
	}
	u, rc, err := a.openUpload(ctx, uploadToken)
	if err != nil {
		return CloudEntry{}, err
	}
	defer rc.Close()
	e, err := a.uploadContent(ctx, c, dir, u.Filename, rc, u.Size)
	return toCloudEntry(e), cloudErr(err)
}

// ImportCloudFile copies a cloud file into an upload so that the composer
// can attach it ("Attach from Nextcloud/OpenCloud").
func (a *App) ImportCloudFile(ctx context.Context, accountID int64, p string) (CloudImportResult, error) {
	rc, e, err := a.OpenCloudFile(ctx, accountID, p)
	if err != nil {
		return CloudImportResult{}, err
	}
	defer rc.Close()
	ct := e.ContentType
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	up, err := a.UploadAttachment(ctx, e.Name, ct, rc)
	if err != nil {
		return CloudImportResult{}, err
	}
	u, err := a.st.GetUpload(ctx, up.UploadToken)
	if err != nil {
		return CloudImportResult{}, err
	}
	return CloudImportResult{UploadToken: up.UploadToken, Filename: u.Filename, MIMEType: u.MIMEType, Size: u.Size}, nil
}

// SaveAttachmentToCloud stores a mail/chat attachment (MessageID + Index)
// or an upload in a cloud directory ("Save to cloud").
func (a *App) SaveAttachmentToCloud(ctx context.Context, in CloudSaveInput) (CloudEntry, error) {
	c, err := a.cloudStorage(ctx, in.AccountID)
	if err != nil {
		return CloudEntry{}, err
	}
	var name string
	var r io.Reader
	var size int64
	switch {
	case in.UploadToken != "":
		u, rc, err := a.openUpload(ctx, in.UploadToken)
		if err != nil {
			return CloudEntry{}, err
		}
		defer rc.Close()
		name, r, size = u.Filename, rc, u.Size
	case in.MessageID != 0:
		at, err := a.MessageAttachment(ctx, in.MessageID, in.Index)
		if err != nil {
			return CloudEntry{}, err
		}
		name, r, size = at.Filename, bytes.NewReader(at.Data), int64(len(at.Data))
	default:
		return CloudEntry{}, invalid("messageId/index or uploadToken required")
	}
	if in.Filename != "" {
		name = in.Filename
	}
	e, err := a.uploadContent(ctx, c, in.Dir, name, r, size)
	return toCloudEntry(e), cloudErr(err)
}

// GenerateCloudPassword returns a share password that satisfies the
// account's password policy.
func (a *App) GenerateCloudPassword(ctx context.Context, accountID int64) (CloudPassword, error) {
	c, err := a.cloudClient(ctx, accountID)
	if err != nil {
		return CloudPassword{}, err
	}
	return CloudPassword{Password: cloud.GeneratePassword(c.passwordPolicy(ctx))}, nil
}

func (c *cloudClient) passwordPolicy(ctx context.Context) cloud.PasswordPolicy {
	if c.policy != nil {
		if p, err := c.policy.PasswordPolicy(ctx); err == nil {
			return p
		}
	}
	return cloud.PasswordPolicy{MinLength: cloud.DefaultPasswordLength}
}

// ==== shares ==================================================================

type shareRequest struct {
	accountID int64
	path      string // stored file (Nextcloud, OpenCloud)
	name      string
	data      []byte // Vaultwarden file Send
	text      *string
	size      int64
	opts      cloud.ShareOptions
	genPass   bool
	source    string
	messageID int64
}

// createShare creates the link and records it in cloud_links (the
// password goes into the secret store).
func (a *App) createShare(ctx context.Context, r shareRequest) (CloudShare, error) {
	c, err := a.cloudClient(ctx, r.accountID)
	if err != nil {
		return CloudShare{}, err
	}
	if r.genPass && r.opts.Password == "" {
		r.opts.Password = cloud.GeneratePassword(c.passwordPolicy(ctx))
	}
	var link cloud.Link
	provider := string(c.kind)
	switch {
	case c.vw != nil:
		so := vaultwarden.SendOptions{Password: r.opts.Password, MaxAccessCount: r.opts.MaxAccessCount, Expiration: r.opts.Expires,
			Deletion: r.opts.DeletionDate, HideEmail: r.opts.HideEmail, Notes: r.opts.Note}
		var s vaultwarden.Send
		if r.text != nil {
			s, err = c.vw.CreateTextSend(ctx, r.name, *r.text, so)
		} else {
			s, err = c.vw.CreateFileSend(ctx, r.name, r.data, so)
		}
		link = cloud.Link{ID: s.ID, URL: s.URL, Password: s.Password, Expires: s.Expires}
	case c.sharer != nil:
		link, err = c.sharer.Share(ctx, r.path, r.opts)
	default:
		return CloudShare{}, Errorf(http.StatusNotImplemented, CodeNotSupported, "%s accounts cannot create share links", c.kind)
	}
	if err != nil {
		return CloudShare{}, cloudErr(err)
	}
	if link.Expires.IsZero() {
		link.Expires = r.opts.Expires
	}
	l := store.CloudLink{AccountID: r.accountID, Provider: provider, RemotePath: link.Path, ShareID: link.ID, URL: link.URL,
		FileName: r.name, MessageID: r.messageID, ExpiresAt: link.Expires, CreatedAt: a.now()}
	if link.Password != "" {
		l.PasswordRef = fmt.Sprintf("cloudlink/%d/%s", a.now().UnixNano(), newID(""))
		if err := a.opts.Secrets.Set(l.PasswordRef, []byte(link.Password)); err != nil {
			l.PasswordRef = ""
		}
	}
	id, err := a.st.CreateCloudLink(ctx, l)
	if err != nil {
		return CloudShare{}, err
	}
	l.ID = id
	_ = a.st.SetCloudLinkMeta(ctx, id, store.CloudLinkMeta{Source: r.source, Size: r.size})
	out := a.toCloudShare(l, store.CloudLinkMeta{Source: r.source, Size: r.size})
	out.Password = link.Password
	return out, nil
}

func (a *App) toCloudShare(l store.CloudLink, m store.CloudLinkMeta) CloudShare {
	src := m.Source
	if src == "" {
		src = store.CloudSourceMail
	}
	name := l.FileName
	if name == "" {
		name = path.Base(l.RemotePath)
	}
	return CloudShare{ID: l.ID, AccountID: l.AccountID, Provider: l.Provider, FileName: name, RemotePath: l.RemotePath, URL: l.URL,
		HasPassword: l.PasswordRef != "", ExpiresAt: rfc3339(l.ExpiresAt), CreatedAt: rfc3339(l.CreatedAt), RevokedAt: rfc3339(l.RevokedAt),
		Active: l.Active(a.now()), Source: src, Size: m.Size, MessageID: l.MessageID}
}

// CreateCloudShare creates a public link from the Files screen.
func (a *App) CreateCloudShare(ctx context.Context, in CloudShareInput) (CloudShare, error) {
	c, err := a.cloudClient(ctx, in.AccountID)
	if err != nil {
		return CloudShare{}, err
	}
	exp, err := parseTime(in.ExpiresAt)
	if err != nil {
		return CloudShare{}, err
	}
	if exp.IsZero() {
		exp = a.now().Add(send.DefaultCloudExpiry)
	}
	if !exp.After(a.now()) {
		return CloudShare{}, invalid("expiry must be in the future")
	}
	r := shareRequest{accountID: in.AccountID, opts: cloud.ShareOptions{Password: in.Password, Expires: exp, Note: in.Note,
		MaxAccessCount: in.MaxAccessCount, HideEmail: in.HideEmail, DeletionDate: exp}, genPass: in.GeneratePassword,
		source: store.CloudSourceFiles, name: in.Name}
	switch {
	case c.vw != nil && in.Text != "":
		t := in.Text
		r.text = &t
		if r.name == "" {
			r.name = "Text"
		}
	case c.vw != nil:
		if in.UploadToken == "" {
			return CloudShare{}, invalid("uploadToken or text required for a Send")
		}
		u, rc, err := a.openUpload(ctx, in.UploadToken)
		if err != nil {
			return CloudShare{}, err
		}
		r.data, err = io.ReadAll(io.LimitReader(rc, maxCloudTransfer))
		rc.Close()
		if err != nil {
			return CloudShare{}, err
		}
		if r.name == "" {
			r.name = u.Filename
		}
		r.size = int64(len(r.data))
	default:
		if cloud.Clean(in.Path) == "/" {
			return CloudShare{}, invalid("path required")
		}
		r.path = cloud.Clean(in.Path)
		if e, err := c.storage.Stat(ctx, r.path); err == nil {
			r.size = e.Size
		} else {
			return CloudShare{}, cloudErr(err)
		}
		if r.name == "" {
			r.name = path.Base(r.path)
		}
	}
	return a.createShare(ctx, r)
}

// ListCloudShares returns all created links, newest first ("My shares").
func (a *App) ListCloudShares(ctx context.Context) ([]CloudShare, error) {
	links, err := a.st.ListCloudLinks(ctx, 0)
	if err != nil {
		return nil, err
	}
	metas, err := a.st.CloudLinkMetas(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CloudShare, 0, len(links))
	for _, l := range links {
		out = append(out, a.toCloudShare(l, metas[l.ID]))
	}
	return out, nil
}

// RevokeCloudShare revokes a link on the server and marks it revoked.
func (a *App) RevokeCloudShare(ctx context.Context, id int64) (CloudShare, error) {
	l, err := a.st.GetCloudLink(ctx, id)
	if err != nil {
		return CloudShare{}, err
	}
	if l.RevokedAt.IsZero() && l.ShareID != "" {
		c, err := a.cloudClient(ctx, l.AccountID)
		if err != nil {
			return CloudShare{}, err
		}
		if c.revoker == nil {
			return CloudShare{}, notSupported("revoking this link")
		}
		if err := c.revoker.Revoke(ctx, l.ShareID); err != nil {
			return CloudShare{}, cloudErr(err)
		}
	}
	if err := a.st.MarkCloudLinkRevoked(ctx, id, a.now()); err != nil {
		return CloudShare{}, err
	}
	if l.PasswordRef != "" {
		_ = a.opts.Secrets.Delete(l.PasswordRef)
	}
	l, err = a.st.GetCloudLink(ctx, id)
	if err != nil {
		return CloudShare{}, err
	}
	metas, _ := a.st.CloudLinkMetas(ctx)
	return a.toCloudShare(l, metas[id]), nil
}

// CloudSharePassword returns the stored password of an active link.
func (a *App) CloudSharePassword(ctx context.Context, id int64) (CloudPassword, error) {
	l, err := a.st.GetCloudLink(ctx, id)
	if err != nil {
		return CloudPassword{}, err
	}
	if l.PasswordRef == "" {
		return CloudPassword{}, errNotFound
	}
	pw, err := a.opts.Secrets.Get(l.PasswordRef)
	if err != nil {
		return CloudPassword{}, err
	}
	return CloudPassword{Password: string(pw)}, nil
}

// linkSharesToMessage records the sent mail on the links inserted into it.
func (a *App) linkSharesToMessage(ctx context.Context, shares []send.ShareResult, messageID int64) {
	for _, sh := range shares {
		if sh.LinkID == 0 {
			continue
		}
		if messageID != 0 {
			_ = a.st.SetCloudLinkMessage(ctx, sh.LinkID, messageID)
		}
	}
}

// deliveryAccount resolves a composer delivery mode (nextcloud, opencloud,
// vaultwarden) to the cloud account used for it.
func (a *App) deliveryAccount(ctx context.Context, delivery string) (int64, error) {
	switch delivery {
	case CloudNextcloud, CloudOpenCloud, CloudVaultwarden:
	default:
		return 0, invalid("unknown attachment delivery %q", delivery)
	}
	acc, err := a.defaultCloudAccount(ctx, delivery)
	return acc.ID, err
}

// ==== send.CloudUploader ======================================================

// cloudUploader uploads "send as link" attachments (KONZEPT 12.2):
// Nextcloud/OpenCloud to <uploadDir>/<date>/<subject>/<file> with a
// generated password and expiry, Vaultwarden as encrypted file Send.
type cloudUploader struct{ a *App }

func (u cloudUploader) Upload(ctx context.Context, accountID int64, f send.UploadFile) (send.Share, error) {
	a := u.a
	if accountID == 0 {
		for _, p := range []string{CloudNextcloud, CloudOpenCloud, CloudVaultwarden} {
			if acc, err := a.defaultCloudAccount(ctx, p); err == nil {
				accountID = acc.ID
				break
			}
		}
		if accountID == 0 {
			return send.Share{}, Errorf(http.StatusConflict, "cloud_not_configured", "no cloud account is configured")
		}
	}
	acc, err := a.cloudAccount(ctx, accountID)
	if err != nil {
		return send.Share{}, err
	}
	c, err := a.cloudClient(ctx, accountID)
	if err != nil {
		return send.Share{}, err
	}
	expiry := f.Expiry
	if expiry <= 0 {
		expiry = send.DefaultCloudExpiry
	}
	exp := a.now().Add(expiry)
	pw := cloud.GeneratePassword(c.passwordPolicy(ctx))
	if c.vw != nil {
		data, err := io.ReadAll(io.LimitReader(f.Body, maxCloudTransfer+1))
		if err != nil {
			return send.Share{}, err
		}
		if len(data) > maxCloudTransfer {
			return send.Share{}, Errorf(http.StatusRequestEntityTooLarge, "too_large", "file is too large for a Send")
		}
		s, err := c.vw.CreateFileSend(ctx, f.Name, data, vaultwarden.SendOptions{Password: pw, Expiration: exp, Deletion: exp})
		if err != nil {
			return send.Share{}, cloudErr(err)
		}
		return send.Share{URL: s.URL, Password: pw, Expires: s.Expires, Provider: string(acc.Kind), ShareID: s.ID}, nil
	}
	if c.storage == nil || c.sharer == nil {
		return send.Share{}, Errorf(http.StatusNotImplemented, CodeNotSupported, "%s accounts cannot create share links", acc.Kind)
	}
	subject := cloud.SafeName(f.Subject)
	if subject == "" {
		subject = "Ohne Betreff"
	}
	dir := cloud.Join(uploadDir(acc), a.now().In(a.opts.Location).Format("2006-01-02"), subject)
	e, err := a.uploadContent(ctx, c, dir, f.Name, f.Body, f.Size)
	if err != nil {
		return send.Share{}, cloudErr(err)
	}
	l, err := c.sharer.Share(ctx, e.Path, cloud.ShareOptions{Password: pw, Expires: exp})
	if err != nil {
		return send.Share{}, cloudErr(err)
	}
	if l.Expires.IsZero() {
		l.Expires = exp
	}
	return send.Share{URL: l.URL, Password: l.Password, Expires: l.Expires, Provider: string(acc.Kind), RemotePath: e.Path, ShareID: l.ID}, nil
}

var _ send.CloudUploader = cloudUploader{}
