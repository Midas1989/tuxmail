// SPDX-License-Identifier: AGPL-3.0-or-later

package domain

import "time"

// AccountKind identifies the protocol/provider family of an account.
type AccountKind string

const (
	KindIMAP         AccountKind = "imap"
	KindPOP3         AccountKind = "pop3"
	KindSMTP         AccountKind = "smtp"
	KindJMAP         AccountKind = "jmap"
	KindEAS          AccountKind = "eas"
	KindGraph        AccountKind = "graph"
	KindEWS          AccountKind = "ews"
	KindGmail        AccountKind = "gmail"
	KindCalDAV       AccountKind = "caldav"
	KindCardDAV      AccountKind = "carddav"
	KindNextcloud    AccountKind = "nextcloud"
	KindOpenCloud    AccountKind = "opencloud"
	KindVaultwarden  AccountKind = "vaultwarden"
	KindWhatsApp     AccountKind = "whatsapp"
	KindSignal       AccountKind = "signal"
	KindMatrixBridge AccountKind = "matrix_bridge"
)

// TLSMode describes how a connection is secured.
type TLSMode string

const (
	TLSImplicit TLSMode = "tls"      // e.g. IMAPS 993, SMTPS 465
	TLSStartTLS TLSMode = "starttls" // e.g. IMAP 143, Submission 587
	TLSNone     TLSMode = "none"     // only allowed after explicit user confirmation
)

// AuthMethod describes how to authenticate against a server.
type AuthMethod string

const (
	AuthPassword   AuthMethod = "password"
	AuthOAuth2     AuthMethod = "oauth2"
	AuthClientCert AuthMethod = "client_cert"
)

// ServerConfig is a single endpoint (incoming or outgoing).
type ServerConfig struct {
	Host     string     `json:"host"`
	Port     int        `json:"port"`
	TLS      TLSMode    `json:"tls"`
	Auth     AuthMethod `json:"auth"`
	Username string     `json:"username"`
}

// Account is a configured account of any kind. Secrets are never stored here,
// only a reference into the secret store.
type Account struct {
	ID          int64             `json:"id"`
	Kind        AccountKind       `json:"kind"`
	DisplayName string            `json:"displayName"`
	Color       string            `json:"color,omitempty"`
	Incoming    *ServerConfig     `json:"incoming,omitempty"`
	Outgoing    *ServerConfig     `json:"outgoing,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
	SecretRef   string            `json:"secretRef,omitempty"`
	Enabled     bool              `json:"enabled"`
	SortOrder   int               `json:"sortOrder"`
}

// Identity is a sender address (primary address or alias) of an account.
type Identity struct {
	ID                 int64  `json:"id"`
	AccountID          int64  `json:"accountId"`
	Name               string `json:"name"`
	Email              string `json:"email"`
	ReplyTo            string `json:"replyTo,omitempty"`
	BccSelf            bool   `json:"bccSelf"`
	SMTPAccountID      int64  `json:"smtpAccountId,omitempty"` // optional dedicated outgoing server
	DefaultSignatureID int64  `json:"defaultSignatureId,omitempty"`
	ReplySignatureID   int64  `json:"replySignatureId,omitempty"`
	IsDefault          bool   `json:"isDefault"`
}

// Signature is a reusable mail signature.
type Signature struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	HTML      string `json:"html"`
	Plain     string `json:"plain"`
	Placement string `json:"placement"` // "below_reply" | "above_quote"
	// PlainOverride keeps Plain as entered; otherwise Plain is generated
	// from HTML when the signature is saved.
	PlainOverride bool `json:"plainOverride,omitempty"`
	// Images are the inline images referenced from HTML as cid:<ContentID>
	// (content in the blob store).
	Images []SignatureImage `json:"images,omitempty"`
	// UpdatedAt is when the signature last changed (Unix seconds; 0 = unknown):
	// "newer state wins" of the settings synchronisation.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
}

// SignatureImage is an image of a signature stored in the blob store.
type SignatureImage struct {
	ContentID string `json:"contentId"`
	MIMEType  string `json:"mimeType"`
	Filename  string `json:"filename,omitempty"`
	BlobHash  string `json:"blobHash"`
	Size      int64  `json:"size"`
}

// FolderRole marks special-use folders (RFC 6154).
type FolderRole string

const (
	RoleNone    FolderRole = ""
	RoleInbox   FolderRole = "inbox"
	RoleSent    FolderRole = "sent"
	RoleDrafts  FolderRole = "drafts"
	RoleTrash   FolderRole = "trash"
	RoleJunk    FolderRole = "junk"
	RoleArchive FolderRole = "archive"
	// RoleTemplates has no RFC 6154 attribute; it is detected by name or
	// assigned by the user.
	RoleTemplates FolderRole = "templates"
)

// RoleSource says how the role of a folder was determined.
type RoleSource string

const (
	RoleSourceServer RoleSource = "server" // SPECIAL-USE attribute / provider folder type
	RoleSourceName   RoleSource = "name"   // name heuristic ("Gesendet", "Sent Items" ...)
	RoleSourceUser   RoleSource = "user"   // manual override (store folder_role_overrides)
)

// Folder is a mailbox/folder on a mail account.
type Folder struct {
	ID        int64      `json:"id"`
	AccountID int64      `json:"accountId"`
	ParentID  int64      `json:"parentId,omitempty"`
	RemoteID  string     `json:"remoteId"` // IMAP path, EAS ServerId, Graph id ...
	Name      string     `json:"name"`
	Delimiter string     `json:"delimiter,omitempty"`
	Role      FolderRole `json:"role,omitempty"`
	// RoleSource tells where Role came from ("" with a role = server).
	RoleSource RoleSource `json:"roleSource,omitempty"`
	Subscribed bool       `json:"subscribed"`
	Unread     int        `json:"unread"`
	Total      int        `json:"total"`
}

// Flag is a message flag/keyword.
type Flag string

const (
	FlagSeen      Flag = `\Seen`
	FlagAnswered  Flag = `\Answered`
	FlagFlagged   Flag = `\Flagged`
	FlagDeleted   Flag = `\Deleted`
	FlagDraft     Flag = `\Draft`
	FlagForwarded Flag = `$Forwarded`
)

// Address is a mail address with optional display name.
type Address struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// MessageHeaderInfo contains everything needed for lists and threading,
// without the body.
type MessageHeaderInfo struct {
	MessageID  string    `json:"messageId"`  // normalized, without angle brackets
	InReplyTo  []string  `json:"inReplyTo"`  // normalized
	References []string  `json:"references"` // normalized, oldest first
	Subject    string    `json:"subject"`
	From       []Address `json:"from"`
	To         []Address `json:"to"`
	Cc         []Address `json:"cc"`
	Bcc        []Address `json:"bcc,omitempty"`
	ReplyTo    []Address `json:"replyTo,omitempty"`
	Date       time.Time `json:"date"`
	// Server-side thread/object hints, if available.
	ServerObjectID string `json:"serverObjectId,omitempty"` // EMAILID, X-GM-MSGID, Graph immutableId
	ServerThreadID string `json:"serverThreadId,omitempty"` // X-GM-THRID, Graph conversationId, EAS ConversationId
	ThreadIndex    string `json:"threadIndex,omitempty"`    // Outlook Thread-Index header (base64)
}

// RemoteRef locates a message on the server.
type RemoteRef struct {
	FolderRemoteID string `json:"folder"`
	ID             string `json:"id"` // IMAP UID as decimal string, EAS ServerId, Graph id ...
}

// RemoteMessage is a message as seen in a sync delta.
type RemoteMessage struct {
	Ref            RemoteRef         `json:"ref"`
	Header         MessageHeaderInfo `json:"header"`
	Flags          []Flag            `json:"flags"`
	Size           int64             `json:"size"`
	ReceivedAt     time.Time         `json:"receivedAt"`
	HasAttachments bool              `json:"hasAttachments"`
}

// SyncState is opaque, provider-specific sync state (UIDVALIDITY+MODSEQ, SyncKey, deltaLink ...).
type SyncState string

// SyncDelta is the result of an incremental folder sync.
type SyncDelta struct {
	Added        []RemoteMessage `json:"added"`
	FlagsChanged []RemoteMessage `json:"flagsChanged"` // only Ref and Flags are meaningful
	Removed      []RemoteRef     `json:"removed"`
	// FullResync is true when the server invalidated the previous state (e.g. UIDVALIDITY changed).
	FullResync bool `json:"fullResync"`
	// More is true when the provider returned only a part of the new
	// messages (the newest); the caller stores the returned state and calls
	// Sync again for the rest.
	More bool `json:"more,omitempty"`
	// Total is the number of messages in the folder on the server (0 = unknown).
	Total int `json:"total,omitempty"`
}

// ChangeHint is emitted by Watch when the server signals changes.
type ChangeHint struct {
	FolderRemoteID string
}

// MailCaps lists optional capabilities of a mail provider.
type MailCaps struct {
	Move      bool `json:"move"`
	Idle      bool `json:"idle"`
	ObjectID  bool `json:"objectId"`
	CondStore bool `json:"condStore"`
	Thread    bool `json:"thread"`
	Sieve     bool `json:"sieve"`
}
