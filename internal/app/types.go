// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"encoding/json"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// Go mirror of the view-level DTOs in frontend/src/lib/types.ts. JSON field
// names must match exactly; slices that are required in TypeScript are
// always non-nil so they serialize as [] and never as null. Times are RFC
// 3339 strings (UTC).

// Account, Identity, Signature, Folder, Address and ServerConfig are the
// domain types (their JSON tags already match types.ts).
type (
	Account      = domain.Account
	Signature    = domain.Signature
	Folder       = domain.Folder
	Address      = domain.Address
	ServerConfig = domain.ServerConfig
	Flag         = domain.Flag
)

// Identity extends domain.Identity with the chat-mode signature settings.
type Identity struct {
	domain.Identity
	ChatSignatureMode string `json:"chatSignatureMode,omitempty"`
	ChatSignatureID   int64  `json:"chatSignatureId,omitempty"`
}

// PersonHandle is a channel address of a person.
type PersonHandle struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Person is a cross-channel person.
type Person struct {
	ID          int64          `json:"id"`
	DisplayName string         `json:"displayName"`
	IsMe        bool           `json:"isMe"`
	Handles     []PersonHandle `json:"handles"`
	AvatarURL   string         `json:"avatarUrl,omitempty"`
	LastSeen    string         `json:"lastSeen,omitempty"`
}

// CloudLinkInfo describes a cloud share link attachment.
type CloudLinkInfo struct {
	Provider  string `json:"provider"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// Attachment is an attachment of a mail or chat message.
type Attachment struct {
	ID          int64          `json:"id"`
	Filename    string         `json:"filename"`
	MIMEType    string         `json:"mimeType"`
	Size        int64          `json:"size"`
	ContentID   string         `json:"contentId,omitempty"`
	Inline      bool           `json:"inline,omitempty"`
	CloudLink   *CloudLinkInfo `json:"cloudLink,omitempty"`
	PreviewURL  string         `json:"previewUrl,omitempty"`
	Width       int            `json:"width,omitempty"`
	Height      int            `json:"height,omitempty"`
	DurationSec float64        `json:"durationSec,omitempty"`
	Waveform    []float64      `json:"waveform,omitempty"`
	URL         string         `json:"url,omitempty"`
}

// MessageSummary is a row of the classic message list.
type MessageSummary struct {
	ID             int64     `json:"id"`
	AccountID      int64     `json:"accountId"`
	FolderIDs      []int64   `json:"folderIds"`
	ConversationID int64     `json:"conversationId"`
	MessageID      string    `json:"messageId"`
	Subject        string    `json:"subject"`
	From           []Address `json:"from"`
	To             []Address `json:"to"`
	Cc             []Address `json:"cc"`
	Date           string    `json:"date"`
	Preview        string    `json:"preview"`
	Flags          []Flag    `json:"flags"`
	HasAttachments bool      `json:"hasAttachments"`
	Size           int64     `json:"size"`
	TagIDs         []int64   `json:"tagIds,omitempty"`
	ProjectIDs     []int64   `json:"projectIds,omitempty"`
}

// MessageDetail is a message with body and attachments.
type MessageDetail struct {
	MessageSummary
	InReplyTo        []string     `json:"inReplyTo"`
	References       []string     `json:"references"`
	Bcc              []Address    `json:"bcc,omitempty"`
	ReplyTo          []Address    `json:"replyTo,omitempty"`
	HTML             string       `json:"html,omitempty"`
	Text             string       `json:"text,omitempty"`
	Attachments      []Attachment `json:"attachments"`
	HasRemoteContent bool         `json:"hasRemoteContent,omitempty"`
}

// Conversation is a row of the chat list.
type Conversation struct {
	ID                 int64    `json:"id"`
	Kind               string   `json:"kind"`
	Title              string   `json:"title"`
	LastActivity       string   `json:"lastActivity"`
	UnreadCount        int      `json:"unreadCount"`
	Pinned             bool     `json:"pinned"`
	Muted              bool     `json:"muted"`
	Archived           bool     `json:"archived"`
	Channels           []string `json:"channels"`
	ParticipantIDs     []int64  `json:"participantIds"`
	AccountIDs         []int64  `json:"accountIds"`
	Snippet            string   `json:"snippet"`
	LastSenderName     string   `json:"lastSenderName,omitempty"`
	IsGroup            bool     `json:"isGroup"`
	Favorite           bool     `json:"favorite"`
	MarkedUnread       bool     `json:"markedUnread"`
	Blocked            bool     `json:"blocked,omitempty"`
	LastIsOwn          bool     `json:"lastIsOwn"`
	LastStatus         string   `json:"lastStatus,omitempty"`
	LastChannel        string   `json:"lastChannel,omitempty"`
	AvatarURL          string   `json:"avatarUrl,omitempty"`
	LastMedia          string   `json:"lastMedia,omitempty"`
	TagIDs             []int64  `json:"tagIds,omitempty"`
	LastMediaName      string   `json:"lastMediaName,omitempty"`
	PinnedItemIDs      []string `json:"pinnedItemIds,omitempty"`
	SnoozedUntil       string   `json:"snoozedUntil,omitempty"`
	ReturnedFromSnooze bool     `json:"returnedFromSnooze,omitempty"`
	Draft              string   `json:"draft,omitempty"`
	ScheduledCount     int      `json:"scheduledCount,omitempty"`
}

// Reaction is an emoji reaction with its count.
type Reaction struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
	Mine  bool   `json:"mine,omitempty"`
}

// LinkPreview is a preview of a link in a message.
type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	SiteName    string `json:"siteName,omitempty"`
	ImageURL    string `json:"imageUrl,omitempty"`
}

// ItemCard is the compact rendering of a newsletter-like mail.
type ItemCard struct {
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
}

// ItemReplyTo is the quoted message a bubble answers.
type ItemReplyTo struct {
	ItemID     string `json:"itemId"`
	SenderName string `json:"senderName"`
	Excerpt    string `json:"excerpt"`
}

// ItemSystem is a system line in the timeline.
type ItemSystem struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Start string `json:"start"`
}

// TimeSlot is a time range.
type TimeSlot struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// MeetingProposal is a meeting proposal with selectable slots.
type MeetingProposal struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	DurationMin int        `json:"durationMin"`
	Slots       []TimeSlot `json:"slots"`
	Status      string     `json:"status"`
	Chosen      string     `json:"chosen,omitempty"`
}

// ConversationItem is one bubble in the chat timeline.
type ConversationItem struct {
	ID             string           `json:"id"`
	ConversationID int64            `json:"conversationId"`
	Channel        string           `json:"channel"`
	AccountID      int64            `json:"accountId"`
	SenderPersonID int64            `json:"senderPersonId"`
	SenderName     string           `json:"senderName"`
	IsOwn          bool             `json:"isOwn"`
	SentAt         string           `json:"sentAt"`
	Body           string           `json:"body"`
	Quoted         string           `json:"quoted,omitempty"`
	Subject        string           `json:"subject,omitempty"`
	Relation       string           `json:"relation,omitempty"`
	Attachments    []Attachment     `json:"attachments"`
	MessageID      int64            `json:"messageId,omitempty"`
	Status         string           `json:"status,omitempty"`
	Reactions      []Reaction       `json:"reactions,omitempty"`
	Unread         bool             `json:"unread,omitempty"`
	Display        string           `json:"display,omitempty"`
	Card           *ItemCard        `json:"card,omitempty"`
	ReplyTo        *ItemReplyTo     `json:"replyTo,omitempty"`
	LinkPreview    *LinkPreview     `json:"linkPreview,omitempty"`
	Signature      string           `json:"signature,omitempty"`
	Deleted        bool             `json:"deleted,omitempty"`
	Starred        bool             `json:"starred,omitempty"`
	Pinned         bool             `json:"pinned,omitempty"`
	ScheduledFor   string           `json:"scheduledFor,omitempty"`
	ScheduledID    string           `json:"scheduledId,omitempty"`
	System         *ItemSystem      `json:"system,omitempty"`
	Meeting        *MeetingProposal `json:"meeting,omitempty"`
	Encrypted      bool             `json:"encrypted,omitempty"`
	FolderIDs      []int64          `json:"folderIds,omitempty"`
	TagIDs         []int64          `json:"tagIds,omitempty"`
	InScope        *bool            `json:"inScope,omitempty"`
}

// ConversationPatch changes chat-mode flags of a conversation. SnoozedUntil
// distinguishes "absent" from null (wake up now) via json.RawMessage.
type ConversationPatch struct {
	SnoozedUntil json.RawMessage `json:"snoozedUntil,omitempty"`
	Pinned       *bool           `json:"pinned,omitempty"`
	Muted        *bool           `json:"muted,omitempty"`
	Archived     *bool           `json:"archived,omitempty"`
	Favorite     *bool           `json:"favorite,omitempty"`
	MarkedUnread *bool           `json:"markedUnread,omitempty"`
	Blocked      *bool           `json:"blocked,omitempty"`
}

// ContactCard holds fields parsed from a mail signature.
type ContactCard struct {
	Name    string   `json:"name,omitempty"`
	Title   string   `json:"title,omitempty"`
	Company string   `json:"company,omitempty"`
	Phones  []string `json:"phones"`
	Emails  []string `json:"emails"`
	Address string   `json:"address,omitempty"`
	Website string   `json:"website,omitempty"`
}

// Page is one page of a list.
type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Offset int `json:"offset"`
}

// OutgoingAttachment is an uploaded attachment to send.
type OutgoingAttachment struct {
	UploadToken string  `json:"uploadToken"`
	Filename    string  `json:"filename"`
	MIMEType    string  `json:"mimeType"`
	Size        int64   `json:"size"`
	Delivery    string  `json:"delivery"`
	DurationSec float64 `json:"durationSec,omitempty"`
}

// PasswordDelivery configures how share passwords are delivered.
type PasswordDelivery struct {
	Mode                 string `json:"mode"`
	MessengerAccountID   int64  `json:"messengerAccountId,omitempty"`
	RememberForRecipient bool   `json:"rememberForRecipient"`
}

// OutgoingMessage is a message to send (mail or messenger).
type OutgoingMessage struct {
	Channel            string               `json:"channel"`
	IdentityID         int64                `json:"identityId,omitempty"`
	AccountID          int64                `json:"accountId,omitempty"`
	ConversationID     int64                `json:"conversationId,omitempty"`
	To                 []Address            `json:"to"`
	Cc                 []Address            `json:"cc"`
	Bcc                []Address            `json:"bcc"`
	Subject            string               `json:"subject"`
	HTML               string               `json:"html"`
	Text               string               `json:"text"`
	InReplyToMessageID int64                `json:"inReplyToMessageId,omitempty"`
	ForwardOfMessageID int64                `json:"forwardOfMessageId,omitempty"`
	Attachments        []OutgoingAttachment `json:"attachments"`
	PasswordDelivery   *PasswordDelivery    `json:"passwordDelivery,omitempty"`
	ReplyToItemID      string               `json:"replyToItemId,omitempty"`
	SignatureID        *int64               `json:"signatureId,omitempty"`
	SendAt             string               `json:"sendAt,omitempty"`
}

// SharePassword is a generated cloud share password.
type SharePassword struct {
	Filename string `json:"filename"`
	Password string `json:"password"`
}

// SendResult is the result of sending.
type SendResult struct {
	MessageID      int64           `json:"messageId,omitempty"`
	ConversationID int64           `json:"conversationId,omitempty"`
	ItemID         string          `json:"itemId,omitempty"`
	Queued         bool            `json:"queued"`
	SharePasswords []SharePassword `json:"sharePasswords,omitempty"`
	ScheduledID    string          `json:"scheduledId,omitempty"`
	// Blocked is set when send checks (KONZEPT 8.13) blocked the message;
	// Findings lists the check results (also warnings of sent messages).
	Blocked  bool          `json:"blocked,omitempty"`
	Findings []SendFinding `json:"findings,omitempty"`
}

// SendFinding is a send-check result.
type SendFinding struct {
	Check    string            `json:"check"`
	Severity string            `json:"severity"` // info | warn | block
	Key      string            `json:"key"`      // i18n key, e.g. sendcheck.missing_subject
	Message  string            `json:"message"`  // English fallback
	Params   map[string]string `json:"params,omitempty"`
}

// DiscoveryResult is the result of account autodiscovery.
type DiscoveryResult struct {
	Source         string        `json:"source"`
	Kind           string        `json:"kind"`
	DisplayName    string        `json:"displayName"`
	Incoming       *ServerConfig `json:"incoming,omitempty"`
	Outgoing       *ServerConfig `json:"outgoing,omitempty"`
	OAuthAvailable bool          `json:"oauthAvailable,omitempty"`
	// OAuthProvider names the sign-in service ("microsoft", "google").
	OAuthProvider string `json:"oauthProvider,omitempty"`
}

// NewAccountInput creates an account.
type NewAccountInput struct {
	Kind        string        `json:"kind"`
	DisplayName string        `json:"displayName"`
	Email       string        `json:"email"`
	Name        string        `json:"name"`
	Password    string        `json:"password,omitempty"`
	Incoming    *ServerConfig `json:"incoming,omitempty"`
	Outgoing    *ServerConfig `json:"outgoing,omitempty"`
}

// ==== Push / notifications =====================================================

// PushSubscriptionInput is a Web Push subscription.
type PushSubscriptionInput struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	UserAgent  string `json:"userAgent,omitempty"`
	DeviceName string `json:"deviceName,omitempty"`
}

// QuietHours is a daily quiet period.
type QuietHours struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// NotificationSettings are the notification preferences.
type NotificationSettings struct {
	Privacy    string          `json:"privacy"`
	QuietHours QuietHours      `json:"quietHours"`
	Accounts   map[string]bool `json:"accounts"`
	Channels   map[string]bool `json:"channels"`
}

// ==== Device flow / provisioning ==============================================

// DeviceAuthorization is an RFC 8628 device authorization.
type DeviceAuthorization struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// ProvisioningWorkflow is a workflow offered by a server.
type ProvisioningWorkflow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ProvisioningProfile is what a server offers to set up on this device.
type ProvisioningProfile struct {
	Server struct {
		URL     string `json:"url"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"server"`
	User struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"user"`
	Accounts   []Account              `json:"accounts"`
	Identities []Identity             `json:"identities"`
	Signatures []Signature            `json:"signatures"`
	Workflows  []ProvisioningWorkflow `json:"workflows"`
}

// DeviceTokenResult is the result of polling a device token or logging in.
type DeviceTokenResult struct {
	Status  string               `json:"status"`
	Profile *ProvisioningProfile `json:"profile,omitempty"`
	Error   string               `json:"error,omitempty"`
}

// ServerLoginInput is a direct login at a TuxMail server.
type ServerLoginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp,omitempty"`
}

// ProvisioningImport selects what to import from a server profile.
type ProvisioningImport struct {
	ServerURL    string   `json:"serverUrl"`
	Mode         string   `json:"mode"`
	AccountIDs   []int64  `json:"accountIds"`
	IdentityIDs  []int64  `json:"identityIds"`
	SignatureIDs []int64  `json:"signatureIds"`
	WorkflowIDs  []string `json:"workflowIds"`
}

// ==== Search ===================================================================

// SearchQuery is a search request of the UI.
type SearchQuery struct {
	Text              string   `json:"text"`
	Mode              string   `json:"mode"`
	Scopes            []string `json:"scopes"`
	AccountIDs        []int64  `json:"accountIds"`
	FolderIDs         []int64  `json:"folderIds"`
	IncludeSubfolders bool     `json:"includeSubfolders"`
	ExcludeTrashSpam  bool     `json:"excludeTrashSpam"`
	From              string   `json:"from,omitempty"`
	To                string   `json:"to,omitempty"`
	Location          string   `json:"location"`
	Sort              string   `json:"sort"`
	Cursor            string   `json:"cursor,omitempty"`
	Limit             int      `json:"limit,omitempty"`
}

// SearchHit is one search result.
type SearchHit struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Title           string   `json:"title"`
	Snippet         string   `json:"snippet"`
	Highlights      [][2]int `json:"highlights"`
	TitleHighlights [][2]int `json:"titleHighlights"`
	Date            string   `json:"date,omitempty"`
	AccountID       int64    `json:"accountId,omitempty"`
	FolderID        int64    `json:"folderId,omitempty"`
	Channel         string   `json:"channel,omitempty"`
	Sender          string   `json:"sender,omitempty"`
	Size            int64    `json:"size,omitempty"`
	HasAttachments  bool     `json:"hasAttachments,omitempty"`
	Unread          bool     `json:"unread,omitempty"`
	Score           float64  `json:"score"`
	MessageID       int64    `json:"messageId,omitempty"`
	ConversationID  int64    `json:"conversationId,omitempty"`
	ItemID          string   `json:"itemId,omitempty"`
	PersonID        int64    `json:"personId,omitempty"`
}

// FacetBucket is one facet value.
type FacetBucket struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Count  int    `json:"count"`
	Clause string `json:"clause"`
}

// SearchFacets are the facets of a result.
type SearchFacets struct {
	Account    []FacetBucket `json:"account"`
	Folder     []FacetBucket `json:"folder"`
	Sender     []FacetBucket `json:"sender"`
	Domain     []FacetBucket `json:"domain"`
	Channel    []FacetBucket `json:"channel"`
	Type       []FacetBucket `json:"type"`
	Attachment []FacetBucket `json:"attachment"`
}

// SearchError is an expert query error.
type SearchError struct {
	Message  string `json:"message"`
	Position int    `json:"position"`
	Code     string `json:"code,omitempty"`
	Token    string `json:"token,omitempty"`
}

// SearchResult is the result of a search.
type SearchResult struct {
	Hits       []SearchHit   `json:"hits"`
	Total      int           `json:"total"`
	Facets     SearchFacets  `json:"facets"`
	Histogram  []FacetBucket `json:"histogram"`
	NextCursor string        `json:"nextCursor,omitempty"`
	Error      *SearchError  `json:"error,omitempty"`
	Fallback   bool          `json:"fallback,omitempty"`
	TookMs     int64         `json:"tookMs"`
}

// SearchProfile is a saved search.
type SearchProfile struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Query  SearchQuery `json:"query"`
	Pinned bool        `json:"pinned"`
}

// SearchHistoryEntry is an entry of the search history.
type SearchHistoryEntry struct {
	Text string `json:"text"`
	Mode string `json:"mode"`
	At   string `json:"at"`
}

// ==== Preferences & locales ====================================================

// RegionSettings are the region preferences. Week start is "locale" or a
// number, so it is kept as raw JSON.
type RegionSettings struct {
	TimeZone          string          `json:"timeZone"`
	DateFormat        string          `json:"dateFormat"`
	CustomDatePattern string          `json:"customDatePattern"`
	TimeFormat        string          `json:"timeFormat"`
	WeekStart         json.RawMessage `json:"weekStart"`
	NumberFormat      string          `json:"numberFormat"`
	RelativeTimes     bool            `json:"relativeTimes"`
	SecondaryTimeZone string          `json:"secondaryTimeZone"`
}

// UserPreferences are the preferences synced with the core.
type UserPreferences struct {
	Locale string         `json:"locale"`
	Region RegionSettings `json:"region"`
}

// LocaleInfo describes a language file.
type LocaleInfo struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	NativeName string `json:"nativeName"`
	Direction  string `json:"direction"`
}

// ==== Chat-mode features =======================================================

// ChatFolderRules are the rules of a chat folder.
type ChatFolderRules struct {
	IncludeConversationIDs []int64  `json:"includeConversationIds"`
	IncludePersonIDs       []int64  `json:"includePersonIds"`
	ExcludeConversationIDs []int64  `json:"excludeConversationIds"`
	Channels               []string `json:"channels"`
	AccountIDs             []int64  `json:"accountIds"`
	Groups                 string   `json:"groups"`
	UnreadOnly             bool     `json:"unreadOnly"`
	ExcludeMuted           bool     `json:"excludeMuted"`
	ExcludeArchived        bool     `json:"excludeArchived"`
}

// ChatFolder is a user-defined chat list.
type ChatFolder struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Emoji          string          `json:"emoji,omitempty"`
	Rules          ChatFolderRules `json:"rules"`
	Order          int             `json:"order"`
	UnreadCount    *int            `json:"unreadCount,omitempty"`
	SearchFolderID int64           `json:"searchFolderId,omitempty"`
}

// StarredItem is a starred item with its conversation title.
type StarredItem struct {
	Item              ConversationItem `json:"item"`
	ConversationTitle string           `json:"conversationTitle"`
}

// FocusSchedule is the automatic focus schedule.
type FocusSchedule struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Days    []int  `json:"days"`
}

// FocusSettings configure focus mode.
type FocusSettings struct {
	Enabled            bool           `json:"enabled"`
	VIPPersonIDs       []int64        `json:"vipPersonIds"`
	VIPConversationIDs []int64        `json:"vipConversationIds"`
	IncludeFrequent    bool           `json:"includeFrequent"`
	Schedule           *FocusSchedule `json:"schedule,omitempty"`
}

// ChatDraft is the unsent composer text.
type ChatDraft struct {
	Text          string `json:"text"`
	ReplyToItemID string `json:"replyToItemId,omitempty"`
	ChannelKey    string `json:"channelKey,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
}

// ScheduledMessage is a message scheduled for later.
type ScheduledMessage struct {
	ID                string `json:"id"`
	ConversationID    int64  `json:"conversationId"`
	ConversationTitle string `json:"conversationTitle"`
	Channel           string `json:"channel"`
	SendAt            string `json:"sendAt"`
	Text              string `json:"text"`
	ItemID            string `json:"itemId"`
}

// ScheduledPatch changes a scheduled message.
type ScheduledPatch struct {
	Text   *string `json:"text,omitempty"`
	SendAt *string `json:"sendAt,omitempty"`
}

// Snippet is a quick reply.
type Snippet struct {
	ID      string `json:"id"`
	Trigger string `json:"trigger"`
	Title   string `json:"title"`
	Text    string `json:"text"`
}

// AiSettings configure the AI assistance.
type AiSettings struct {
	Provider           string          `json:"provider"`
	BaseURL            string          `json:"baseUrl"`
	Model              string          `json:"model"`
	APIKey             string          `json:"apiKey,omitempty"`
	APIKeySet          bool            `json:"apiKeySet"`
	AccountPermissions map[string]bool `json:"accountPermissions"`
	MaskPersonalData   bool            `json:"maskPersonalData"`
}

// AiSummary is a conversation summary.
type AiSummary struct {
	Summary string   `json:"summary"`
	Bullets []string `json:"bullets"`
	Skipped int      `json:"skipped"`
}

// AiTranslation is a translated item.
type AiTranslation struct {
	Text       string `json:"text"`
	SourceLang string `json:"sourceLang"`
}

// FreeSlotQuery asks for free time slots.
type FreeSlotQuery struct {
	From        string `json:"from"`
	To          string `json:"to"`
	DurationMin int    `json:"durationMin"`
}

// MeetingProposalInput proposes a meeting in a conversation.
type MeetingProposalInput struct {
	Title       string     `json:"title"`
	DurationMin int        `json:"durationMin"`
	Slots       []TimeSlot `json:"slots"`
	Channel     string     `json:"channel"`
	IdentityID  int64      `json:"identityId,omitempty"`
	AccountID   int64      `json:"accountId,omitempty"`
}

// ReactionResult is the result of toggling a reaction.
type ReactionResult struct {
	Reactions     []Reaction `json:"reactions"`
	SentAsRfc9078 bool       `json:"sentAsRfc9078,omitempty"`
}

// ConversationRef locates a bubble.
type ConversationRef struct {
	ConversationID int64  `json:"conversationId"`
	ItemID         string `json:"itemId"`
}

// IDResult carries a created id.
type IDResult struct {
	ID string `json:"id"`
}

// UploadResult carries an upload token.
type UploadResult struct {
	UploadToken string `json:"uploadToken"`
}

// VaultStatus describes the secret store of the desktop client.
type VaultStatus struct {
	// Kind is "keyring" (installed), "vault" (portable) or "server".
	Kind        string `json:"kind"`
	Portable    bool   `json:"portable"`
	Locked      bool   `json:"locked"`
	Initialized bool   `json:"initialized"`
}

// ==== Items, tags, projects, search folders ===================================

// ItemRef references an item of any kind.
type ItemRef struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

// Tag is a hierarchical tag.
type Tag struct {
	ID        int64  `json:"id"`
	ParentID  int64  `json:"parentId,omitempty"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Color     string `json:"color"`
	Icon      string `json:"icon,omitempty"`
	SortOrder int    `json:"sortOrder"`
}

// TagCount holds the counts of a tag.
type TagCount struct {
	Tag    Tag `json:"tag"`
	Direct int `json:"direct"`
	Total  int `json:"total"`
	Unread int `json:"unread"`
}

// TagInput creates or updates a tag.
type TagInput struct {
	ID    int64  `json:"id,omitempty"`
	Path  string `json:"path"`
	Color string `json:"color"`
	Icon  string `json:"icon,omitempty"`
}

// CustomField is a project custom field.
type CustomField struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Value   string   `json:"value"`
	Options []string `json:"options,omitempty"`
}

// ProjectBudget is a project budget.
type ProjectBudget struct {
	Amount   float64  `json:"amount"`
	Currency string   `json:"currency"`
	Spent    *float64 `json:"spent,omitempty"`
}

// ProjectCloudFolder is the linked cloud folder.
type ProjectCloudFolder struct {
	Provider string `json:"provider"`
	Path     string `json:"path"`
	URL      string `json:"url,omitempty"`
}

// ProjectRules is the auto-assign rule of a project.
type ProjectRules struct {
	Enabled bool   `json:"enabled"`
	Query   string `json:"query"`
}

// ProjectCounts are linked item counts.
type ProjectCounts struct {
	Mail  int `json:"mail"`
	Chat  int `json:"chat"`
	Event int `json:"event"`
	Task  int `json:"task"`
	File  int `json:"file"`
}

// Project is a project folder.
type Project struct {
	ID               int64               `json:"id"`
	Key              string              `json:"key"`
	Name             string              `json:"name"`
	Status           string              `json:"status"`
	Color            string              `json:"color"`
	Description      string              `json:"description"`
	Customer         string              `json:"customer,omitempty"`
	CustomerPersonID int64               `json:"customerPersonId,omitempty"`
	StartDate        string              `json:"startDate,omitempty"`
	DueDate          string              `json:"dueDate,omitempty"`
	Budget           *ProjectBudget      `json:"budget,omitempty"`
	CustomFields     []CustomField       `json:"customFields"`
	MemberPersonIDs  []int64             `json:"memberPersonIds"`
	CloudFolder      *ProjectCloudFolder `json:"cloudFolder,omitempty"`
	Notes            string              `json:"notes"`
	TagIDs           []int64             `json:"tagIds"`
	Rules            ProjectRules        `json:"rules"`
	Progress         float64             `json:"progress"`
	Counts           ProjectCounts       `json:"counts"`
	TemplateID       string              `json:"templateId,omitempty"`
	CreatedAt        string              `json:"createdAt"`
	UpdatedAt        string              `json:"updatedAt"`
}

// ProjectTemplate is a template for new projects.
type ProjectTemplate struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Description         string        `json:"description"`
	Color               string        `json:"color"`
	Fields              []CustomField `json:"fields"`
	WorkflowTemplateIDs []string      `json:"workflowTemplateIds"`
	RuleQuery           string        `json:"ruleQuery,omitempty"`
}

// ProjectTimelineEntry is an entry of a project timeline.
type ProjectTimelineEntry struct {
	Item           ItemRef `json:"item"`
	Kind           string  `json:"kind"`
	Date           string  `json:"date"`
	Title          string  `json:"title"`
	Snippet        string  `json:"snippet,omitempty"`
	PersonID       int64   `json:"personId,omitempty"`
	ActorName      string  `json:"actorName,omitempty"`
	Channel        string  `json:"channel,omitempty"`
	ConversationID int64   `json:"conversationId,omitempty"`
	MessageID      int64   `json:"messageId,omitempty"`
	ItemID         string  `json:"itemId,omitempty"`
	Done           *bool   `json:"done,omitempty"`
	Due            string  `json:"due,omitempty"`
	End            string  `json:"end,omitempty"`
	Location       string  `json:"location,omitempty"`
	Path           string  `json:"path,omitempty"`
	Size           int64   `json:"size,omitempty"`
	Source         string  `json:"source"`
}

// SearchFolder is a saved search shown as a folder.
type SearchFolder struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Query           string `json:"query"`
	AccountID       int64  `json:"accountId,omitempty"`
	ProjectID       int64  `json:"projectId,omitempty"`
	UseAsChatFolder bool   `json:"useAsChatFolder"`
	Icon            string `json:"icon,omitempty"`
	Color           string `json:"color,omitempty"`
	Total           int    `json:"total"`
	Unread          int    `json:"unread"`
}

// SearchFolderInput creates or updates a search folder.
type SearchFolderInput struct {
	ID              int64  `json:"id,omitempty"`
	Name            string `json:"name"`
	Query           string `json:"query"`
	AccountID       int64  `json:"accountId,omitempty"`
	ProjectID       int64  `json:"projectId,omitempty"`
	UseAsChatFolder bool   `json:"useAsChatFolder"`
	Icon            string `json:"icon,omitempty"`
	Color           string `json:"color,omitempty"`
}

// ==== Workflows ================================================================

// WorkflowTrigger selects events.
type WorkflowTrigger struct {
	Event    string   `json:"event"`
	Accounts []int64  `json:"accounts,omitempty"`
	Folders  []string `json:"folders,omitempty"`
	Channels []string `json:"channels,omitempty"`
	Schedule string   `json:"schedule,omitempty"`
	Name     string   `json:"name,omitempty"`
}

// WorkflowRule is a structured condition.
type WorkflowRule struct {
	All   []WorkflowRule `json:"all,omitempty"`
	Any   []WorkflowRule `json:"any,omitempty"`
	Not   *WorkflowRule  `json:"not,omitempty"`
	Field string         `json:"field,omitempty"`
	Op    string         `json:"op,omitempty"`
	Value any            `json:"value,omitempty"`
}

// WorkflowCondition is an expression or a rule.
type WorkflowCondition struct {
	Expr string        `json:"expr,omitempty"`
	Rule *WorkflowRule `json:"rule,omitempty"`
}

// WorkflowAction is one action.
type WorkflowAction struct {
	Type            string         `json:"type"`
	Params          map[string]any `json:"params"`
	If              string         `json:"if,omitempty"`
	ContinueOnError bool           `json:"continueOnError,omitempty"`
}

// WorkflowRunSummary is the short form of a run.
type WorkflowRunSummary struct {
	ID        string `json:"id"`
	StartedAt string `json:"startedAt"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

// Workflow is a workflow as edited by the UI.
type Workflow struct {
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Description     string              `json:"description,omitempty"`
	Enabled         bool                `json:"enabled"`
	Trigger         WorkflowTrigger     `json:"trigger"`
	Condition       *WorkflowCondition  `json:"condition,omitempty"`
	Actions         []WorkflowAction    `json:"actions"`
	Options         map[string]any      `json:"options,omitempty"`
	ProjectID       int64               `json:"projectId,omitempty"`
	TemplateID      string              `json:"templateId,omitempty"`
	ExternalEffects bool                `json:"externalEffects,omitempty"`
	LastRun         *WorkflowRunSummary `json:"lastRun,omitempty"`
	UpdatedAt       string              `json:"updatedAt,omitempty"`
}

// WorkflowRunStep is a step of a run.
type WorkflowRunStep struct {
	Action     string `json:"action"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
	DurationMs int64  `json:"durationMs"`
}

// WorkflowRunItem is the item a run was about.
type WorkflowRunItem struct {
	Kind  string `json:"kind"`
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

// WorkflowRun is a run of a workflow.
type WorkflowRun struct {
	WorkflowRunSummary
	WorkflowID string            `json:"workflowId"`
	FinishedAt string            `json:"finishedAt,omitempty"`
	Trigger    string            `json:"trigger"`
	Item       *WorkflowRunItem  `json:"item,omitempty"`
	Steps      []WorkflowRunStep `json:"steps"`
}

// WorkflowPlannedAction is a planned action of a dry run.
type WorkflowPlannedAction struct {
	Type     string            `json:"type"`
	Summary  string            `json:"summary"`
	External bool              `json:"external"`
	Params   map[string]string `json:"params,omitempty"`
}

// WorkflowDryRunItem is the sample item of a dry run.
type WorkflowDryRunItem struct {
	Kind  string `json:"kind"`
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Date  string `json:"date"`
	From  string `json:"from,omitempty"`
}

// WorkflowDryRunSample is one sample of a dry run.
type WorkflowDryRunSample struct {
	Item    WorkflowDryRunItem      `json:"item"`
	Matched bool                    `json:"matched"`
	Planned []WorkflowPlannedAction `json:"planned"`
}

// WorkflowDryRunResult is the result of a dry run.
type WorkflowDryRunResult struct {
	Samples []WorkflowDryRunSample `json:"samples"`
	Matched int                    `json:"matched"`
	Checked int                    `json:"checked"`
}

// WorkflowDiagnostic is a problem in workflow source text.
type WorkflowDiagnostic struct {
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	EndLine  int    `json:"endLine"`
	EndCol   int    `json:"endCol"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Detail   string `json:"detail,omitempty"`
	Message  string `json:"message"`
}

// WorkflowValidation is the result of validating source text.
type WorkflowValidation struct {
	Diagnostics []WorkflowDiagnostic `json:"diagnostics"`
	Workflow    *Workflow            `json:"workflow,omitempty"`
}

// WorkflowCompletionItem is one completion.
type WorkflowCompletionItem struct {
	Label  string `json:"label"`
	Insert string `json:"insert"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// WorkflowCompletion is the result of a completion request.
type WorkflowCompletion struct {
	From  int                      `json:"from"`
	To    int                      `json:"to"`
	Items []WorkflowCompletionItem `json:"items"`
}

// WorkflowParamSpec describes an action parameter for the editors.
type WorkflowParamSpec struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Required   bool     `json:"required,omitempty"`
	Positional bool     `json:"positional,omitempty"`
	Options    []string `json:"options,omitempty"`
}

// WorkflowActionSpec describes an action type.
type WorkflowActionSpec struct {
	Type     string              `json:"type"`
	Group    string              `json:"group"`
	External bool                `json:"external"`
	Triggers []string            `json:"triggers"`
	Params   []WorkflowParamSpec `json:"params"`
}

// WorkflowTriggerSpec describes a trigger event.
type WorkflowTriggerSpec struct {
	Event   string   `json:"event"`
	Group   string   `json:"group"`
	Filters []string `json:"filters"`
}

// WorkflowFieldSpec describes a condition field.
type WorkflowFieldSpec struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Alias string `json:"alias,omitempty"`
}

// WorkflowSchema is everything the editors need.
type WorkflowSchema struct {
	Triggers  []WorkflowTriggerSpec `json:"triggers"`
	Fields    []WorkflowFieldSpec   `json:"fields"`
	Operators map[string][]string   `json:"operators"`
	Actions   []WorkflowActionSpec  `json:"actions"`
	Variables []string              `json:"variables"`
}

// WorkflowTemplate is a workflow template.
type WorkflowTemplate struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Workflow    Workflow `json:"workflow"`
}

// ==== Events ===================================================================

// Event is an ApiEvent (types.ts) pushed to the UI. Only the fields of the
// given type are set.
type Event struct {
	Type           string              `json:"type"`
	MessageID      int64               `json:"messageId,omitempty"`
	ConversationID int64               `json:"conversationId,omitempty"`
	FolderIDs      []int64             `json:"folderIds,omitempty"`
	ItemID         string              `json:"itemId,omitempty"`
	FolderID       int64               `json:"folderId,omitempty"`
	AccountID      int64               `json:"accountId,omitempty"`
	State          string              `json:"state,omitempty"`
	ProjectID      int64               `json:"projectId,omitempty"`
	WorkflowID     string              `json:"workflowId,omitempty"`
	Run            *WorkflowRunSummary `json:"run,omitempty"`
}

// ApiEvent type names.
const (
	EventMessageAdded        = "message.added"
	EventMessageUpdated      = "message.updated"
	EventMessageRemoved      = "message.removed"
	EventConversationUpdated = "conversation.updated"
	EventFolderUpdated       = "folder.updated"
	EventAccountUpdated      = "account.updated"
	EventConnection          = "connection"
	EventTagsChanged         = "tags.changed"
	EventProjectUpdated      = "project.updated"
	EventSearchFoldersChange = "searchfolders.changed"
	EventWorkflowRun         = "workflow.run"
	EventVaultLocked         = "vault.locked"
)
