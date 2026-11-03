// SPDX-License-Identifier: AGPL-3.0-or-later

package app

// Cloud files DTOs (frontend/src/lib/types.ts, section "Cloud").

// Cloud providers as seen by the frontend.
const (
	CloudNextcloud   = "nextcloud"
	CloudOpenCloud   = "opencloud"
	CloudVaultwarden = "vaultwarden"
	CloudWebDAV      = "webdav"
)

// CloudCaps tells the UI what an account can do.
type CloudCaps struct {
	Files bool `json:"files"` // browse/upload/download
	Share bool `json:"share"` // public links of stored files
	Send  bool `json:"send"`  // encrypted Sends of local files/text (Vaultwarden)
}

// CloudDAV are the CalDAV/CardDAV homes of a Nextcloud account (offered to
// the calendar and contacts module).
type CloudDAV struct {
	CalDAV  string `json:"caldav"`
	CardDAV string `json:"carddav"`
}

// CloudAccount is a configured cloud account.
type CloudAccount struct {
	ID           int64     `json:"id"`
	Provider     string    `json:"provider"`
	DisplayName  string    `json:"displayName"`
	Server       string    `json:"server"`
	User         string    `json:"user,omitempty"`
	Capabilities CloudCaps `json:"capabilities"`
	DAV          *CloudDAV `json:"dav,omitempty"`
	// UploadDir is the base directory of attachments sent as links.
	UploadDir string `json:"uploadDir,omitempty"`
}

// CloudLoginInput starts adding a cloud account.
type CloudLoginInput struct {
	Provider    string `json:"provider"`
	Server      string `json:"server"`
	DisplayName string `json:"displayName,omitempty"`
	// RedirectURL (OpenCloud, server mode): the TuxMail callback
	// /api/cloud/oauth/callback; empty on the desktop (loopback).
	RedirectURL string `json:"redirectUrl,omitempty"`
	// ClientID overrides the OIDC client (OpenCloud).
	ClientID string `json:"clientId,omitempty"`
	// Vaultwarden: e-mail, master password, TOTP code; WebDAV: user name, password.
	Email    string `json:"email,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	TOTP     string `json:"totp,omitempty"`
}

// Login flow states.
const (
	CloudFlowPending   = "pending"
	CloudFlowDone      = "done"
	CloudFlowError     = "error"
	CloudFlowTwoFactor = "two_factor"
)

// CloudLoginFlow is a running login (Nextcloud Login Flow v2, OIDC).
type CloudLoginFlow struct {
	ID       string        `json:"id"`
	Provider string        `json:"provider"`
	Status   string        `json:"status"`
	LoginURL string        `json:"loginUrl,omitempty"`
	Error    string        `json:"error,omitempty"`
	Account  *CloudAccount `json:"account,omitempty"`
}

// CloudEntry is a file or directory.
type CloudEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Dir         bool   `json:"dir"`
	Size        int64  `json:"size"`
	Modified    string `json:"modified,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// CloudQuota is the storage quota (-1 = unknown/unlimited).
type CloudQuota struct {
	Used      int64 `json:"used"`
	Available int64 `json:"available"`
}

// CloudListing is the content of a directory.
type CloudListing struct {
	AccountID int64        `json:"accountId"`
	Path      string       `json:"path"`
	Entries   []CloudEntry `json:"entries"`
}

// CloudShareInput creates a share link: Path (stored file) for Nextcloud,
// OpenCloud; UploadToken (file) or Text for Vaultwarden Send.
type CloudShareInput struct {
	AccountID   int64  `json:"accountId"`
	Path        string `json:"path,omitempty"`
	UploadToken string `json:"uploadToken,omitempty"`
	Text        string `json:"text,omitempty"`
	Name        string `json:"name,omitempty"`
	// Password: explicit; GeneratePassword creates one following the
	// server's password policy.
	Password         string `json:"password,omitempty"`
	GeneratePassword bool   `json:"generatePassword,omitempty"`
	ExpiresAt        string `json:"expiresAt,omitempty"`
	Note             string `json:"note,omitempty"`
	MaxAccessCount   int    `json:"maxAccessCount,omitempty"`
	HideEmail        bool   `json:"hideEmail,omitempty"`
}

// CloudShare is a created link ("My shares").
type CloudShare struct {
	ID          int64  `json:"id"`
	AccountID   int64  `json:"accountId"`
	Provider    string `json:"provider"`
	FileName    string `json:"fileName"`
	RemotePath  string `json:"remotePath,omitempty"`
	URL         string `json:"url"`
	Password    string `json:"password,omitempty"` // only right after creation
	HasPassword bool   `json:"hasPassword"`
	ExpiresAt   string `json:"expiresAt,omitempty"`
	CreatedAt   string `json:"createdAt"`
	RevokedAt   string `json:"revokedAt,omitempty"`
	Active      bool   `json:"active"`
	Source      string `json:"source"`
	Size        int64  `json:"size,omitempty"`
	MessageID   int64  `json:"messageId,omitempty"`
}

// CloudSaveInput saves an attachment (mail or chat item, or an upload) to
// a cloud directory ("Save to cloud").
type CloudSaveInput struct {
	AccountID   int64  `json:"accountId"`
	Dir         string `json:"dir"`
	MessageID   int64  `json:"messageId,omitempty"`
	Index       int    `json:"index,omitempty"`
	UploadToken string `json:"uploadToken,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

// CloudImportResult is a cloud file copied into an upload for the composer
// ("Attach from Nextcloud/OpenCloud").
type CloudImportResult struct {
	UploadToken string `json:"uploadToken"`
	Filename    string `json:"filename"`
	MIMEType    string `json:"mimeType"`
	Size        int64  `json:"size"`
}

// CloudPassword is a generated or stored share password.
type CloudPassword struct {
	Password string `json:"password"`
}
