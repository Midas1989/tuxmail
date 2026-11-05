// SPDX-License-Identifier: AGPL-3.0-or-later

// Package wire defines the messenger plugin protocol (KONZEPT 4.4, 11.4):
// the method names, the message types and a small JSON-RPC 2.0 connection
// over a pair of byte streams (the plugin's stdin/stdout).
//
// The service shape follows proto/messenger.proto (MessengerPlugin); the
// encoding is newline-delimited JSON instead of gRPC so that plugins need
// no generated code and no local socket (go-plugin would use a Unix socket
// or, on Windows, a TCP port). Streams of the gRPC service are mapped to
// notifications: pairing updates and live events arrive as "event"
// notifications from the plugin, media is transferred through files in the
// plugin's account directory.
package wire

import "time"

// ProtocolVersion is the version of this protocol. A plugin reports the
// version it implements in HelloResult; the host refuses plugins with a
// different major version.
const ProtocolVersion = 1

// Methods called by the host on the plugin.
const (
	MethodHello         = "hello"               // HelloParams -> HelloResult
	MethodPing          = "ping"                // -> PingResult
	MethodShutdown      = "shutdown"            // -> {}
	MethodListAccounts  = "accounts.list"       // -> AccountList
	MethodConnect       = "accounts.connect"    // AccountRef -> {}
	MethodDisconnect    = "accounts.disconnect" // AccountRef -> {}
	MethodRemoveAccount = "accounts.remove"     // AccountRef -> {} (logs out and deletes the session)
	MethodStartPairing  = "pairing.start"       // PairRequest -> {}; progress as Event{Kind: pairing}
	MethodPairingInput  = "pairing.input"       // PairInput -> {}
	MethodCancelPairing = "pairing.cancel"      // PairingRef -> {}
	MethodListChats     = "chats.list"          // AccountRef -> ChatList
	MethodHistory       = "chats.history"       // HistoryRequest -> MessageList
	MethodTyping        = "chats.typing"        // TypingRequest -> {}
	MethodSend          = "messages.send"       // SendRequest -> SendResult
	MethodReact         = "messages.react"      // ReactRequest -> {}
	MethodEdit          = "messages.edit"       // EditRequest -> {}
	MethodDelete        = "messages.delete"     // DeleteRequest -> {}
	MethodMarkRead      = "messages.mark_read"  // MarkReadRequest -> {}
	MethodDownloadMedia = "media.download"      // MediaRequest -> MediaResult
)

// NotifyEvent is the notification a plugin sends for every Event.
const NotifyEvent = "event"

// Risk levels of a plugin (KONZEPT 18.1).
const (
	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// Pairing methods.
const (
	PairQR       = "qr"       // scan a QR code with the phone (WhatsApp, Signal, Telegram)
	PairPhone    = "phone"    // phone number + code sent by the service (WhatsApp pairing code, Telegram)
	PairPassword = "password" // user name + password (Matrix)
	PairToken    = "token"    // SSO login token (Matrix)
	PairBridge   = "bridge"   // pair a network through a Matrix bridge bot
)

// Capabilities a plugin may announce.
const (
	CapMedia        = "media"
	CapReactions    = "reactions"
	CapReplies      = "replies"
	CapEdit         = "edit"
	CapDelete       = "delete"
	CapReadReceipts = "read_receipts"
	CapTyping       = "typing"
	CapHistory      = "history"
	CapThreads      = "threads"
	CapBridge       = "bridge"
	CapE2EE         = "e2ee"
)

// HelloParams starts a session with a plugin.
type HelloParams struct {
	ProtocolVersion int    `json:"protocolVersion"`
	HostVersion     string `json:"hostVersion"`
	// DataDir is the plugin's data directory (data_dir/plugins/<name>);
	// every account keeps its session in DataDir/<account>.
	DataDir string `json:"dataDir"`
	// Config holds plugin settings (e.g. Telegram api_id, signal-cli path).
	Config map[string]string `json:"config,omitempty"`
	// NetPolicy is the outbound connection policy of the server mode
	// (internal/netguard); nil means unrestricted (desktop mode). The
	// plugin installs it for its own HTTP client so that user-chosen
	// destinations (the Matrix homeserver) cannot reach loopback, private
	// or metadata addresses from the plugin process either.
	NetPolicy *NetPolicy `json:"netPolicy,omitempty"`
}

// NetPolicy is the serialised netguard.Policy.
type NetPolicy struct {
	// AllowPrivate permits private networks and loopback.
	AllowPrivate bool `json:"allowPrivate,omitempty"`
	// Allowed are the always-reachable networks in the notation of
	// security.allowed_networks ("10.0.0.0/8", "[::1]:8008").
	Allowed []string `json:"allowed,omitempty"`
}

// HelloResult describes a plugin.
type HelloResult struct {
	Name            string `json:"name"`
	DisplayName     string `json:"displayName"`
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocolVersion"`
	// Channel is the network of the plugin's chats (a bridge plugin may
	// report other channels per chat).
	Channel        string   `json:"channel"`
	Risk           string   `json:"risk"`
	PairingMethods []string `json:"pairingMethods"`
	Capabilities   []string `json:"capabilities"`
	// UpdateHint is set when the plugin knows it is outdated.
	UpdateHint string `json:"updateHint,omitempty"`
}

// PingResult answers a health check.
type PingResult struct {
	OK       bool `json:"ok"`
	Accounts int  `json:"accounts"`
}

// Account states.
const (
	StateConnected    = "connected"
	StateConnecting   = "connecting"
	StateDisconnected = "disconnected"
	StateLoggedOut    = "logged_out"
	StateError        = "error"
)

// Account is a messenger account (a linked device session) of a plugin.
type Account struct {
	ID      string  `json:"id"`
	Self    Contact `json:"self"`
	State   string  `json:"state"`
	Channel string  `json:"channel,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// AccountRef addresses an account of the plugin.
type AccountRef struct {
	Account string `json:"account"`
}

// AccountList is the result of accounts.list.
type AccountList struct {
	Accounts []Account `json:"accounts"`
}

// Contact is a user on the network.
type Contact struct {
	// ID is the network user id (WhatsApp JID, Telegram user id, Matrix
	// user id, Signal ACI/phone).
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Phone    string `json:"phone,omitempty"` // E.164 if known
	Username string `json:"username,omitempty"`
	Bot      bool   `json:"bot,omitempty"`
}

// Chat kinds.
const (
	ChatDirect  = "direct"
	ChatGroup   = "group"
	ChatChannel = "channel"
)

// Chat is a direct chat, group, channel or Matrix room.
type Chat struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	// Channel overrides the plugin channel (bridged rooms: whatsapp, signal ...).
	Channel      string    `json:"channel,omitempty"`
	Peer         *Contact  `json:"peer,omitempty"` // counterpart of a direct chat
	Unread       int       `json:"unread,omitempty"`
	Muted        bool      `json:"muted,omitempty"`
	Archived     bool      `json:"archived,omitempty"`
	LastActivity time.Time `json:"lastActivity,omitzero"`
	// Bridge is set for Matrix rooms that are portals of a bridge.
	Bridge *BridgeInfo `json:"bridge,omitempty"`
}

// BridgeInfo describes a bridged Matrix room (m.bridge state event).
type BridgeInfo struct {
	Network string `json:"network"` // whatsapp, signal, telegram ...
	Bot     string `json:"bot,omitempty"`
	Remote  string `json:"remote,omitempty"` // remote room/chat id
}

// ChatList is the result of chats.list.
type ChatList struct {
	Chats []Chat `json:"chats"`
}

// Media is an attachment of a message. Ref is opaque to the host and passed
// back to media.download.
type Media struct {
	Ref         string  `json:"ref"`
	MIMEType    string  `json:"mimeType,omitempty"`
	Name        string  `json:"name,omitempty"`
	Size        int64   `json:"size,omitempty"`
	DurationSec float64 `json:"durationSec,omitempty"`
}

// Reaction is an emoji reaction on a message.
type Reaction struct {
	Emoji    string `json:"emoji"`
	SenderID string `json:"senderId"`
	FromMe   bool   `json:"fromMe,omitempty"`
}

// Message delivery states.
const (
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusFailed    = "failed"
)

// Message is a chat message.
type Message struct {
	ChatID    string     `json:"chatId"`
	ID        string     `json:"id"`
	Sender    Contact    `json:"sender"`
	FromMe    bool       `json:"fromMe,omitempty"`
	Time      time.Time  `json:"time"`
	Text      string     `json:"text,omitempty"`
	ReplyTo   string     `json:"replyTo,omitempty"`
	ThreadID  string     `json:"threadId,omitempty"`
	Media     *Media     `json:"media,omitempty"`
	EditedAt  time.Time  `json:"editedAt,omitzero"`
	Deleted   bool       `json:"deleted,omitempty"`
	Reactions []Reaction `json:"reactions,omitempty"`
	Status    string     `json:"status,omitempty"`
	// Service marks system messages (member joined, title changed ...).
	Service bool `json:"service,omitempty"`
	// Historical marks messages from a history sync of a newly linked
	// device: stored without notifications and workflow triggers.
	Historical bool `json:"historical,omitempty"`
}

// MessageList is the result of chats.history.
type MessageList struct {
	Messages []Message `json:"messages"`
}

// HistoryRequest fetches older messages of a chat, newest first.
type HistoryRequest struct {
	Account string `json:"account"`
	ChatID  string `json:"chatId"`
	Before  string `json:"before,omitempty"` // message id; empty = newest
	Limit   int    `json:"limit,omitempty"`
}

// OutMedia is a file to send. Path lies in the account directory of the
// plugin (written by the host).
type OutMedia struct {
	Path        string  `json:"path"`
	MIMEType    string  `json:"mimeType"`
	Name        string  `json:"name"`
	DurationSec float64 `json:"durationSec,omitempty"`
	Voice       bool    `json:"voice,omitempty"`
}

// SendRequest sends a message to ChatID, or (when ChatID is empty) to the
// direct chat with To (e.g. by phone number).
type SendRequest struct {
	Account string    `json:"account"`
	ChatID  string    `json:"chatId,omitempty"`
	To      *Contact  `json:"to,omitempty"`
	Text    string    `json:"text,omitempty"`
	ReplyTo string    `json:"replyTo,omitempty"`
	Media   *OutMedia `json:"media,omitempty"`
}

// SendResult identifies the sent message.
type SendResult struct {
	ChatID string    `json:"chatId"`
	ID     string    `json:"id"`
	Time   time.Time `json:"time"`
}

// ReactRequest sets (or with Remove clears) the own reaction on a message.
type ReactRequest struct {
	Account   string `json:"account"`
	ChatID    string `json:"chatId"`
	MessageID string `json:"messageId"`
	Emoji     string `json:"emoji"`
	Remove    bool   `json:"remove,omitempty"`
	// SenderID is the sender of the message (needed by WhatsApp/Signal).
	SenderID string `json:"senderId,omitempty"`
	FromMe   bool   `json:"fromMe,omitempty"`
}

// EditRequest replaces the text of an own message.
type EditRequest struct {
	Account   string `json:"account"`
	ChatID    string `json:"chatId"`
	MessageID string `json:"messageId"`
	Text      string `json:"text"`
}

// DeleteRequest deletes a message ("for everyone" for own messages).
type DeleteRequest struct {
	Account     string `json:"account"`
	ChatID      string `json:"chatId"`
	MessageID   string `json:"messageId"`
	ForEveryone bool   `json:"forEveryone,omitempty"`
	SenderID    string `json:"senderId,omitempty"`
	FromMe      bool   `json:"fromMe,omitempty"`
}

// MarkReadRequest sends read receipts up to (and including) a message.
type MarkReadRequest struct {
	Account    string   `json:"account"`
	ChatID     string   `json:"chatId"`
	MessageIDs []string `json:"messageIds"`
	// SenderID of the newest message (WhatsApp groups need it).
	SenderID string    `json:"senderId,omitempty"`
	Time     time.Time `json:"time,omitzero"`
}

// TypingRequest reports the user's own typing state.
type TypingRequest struct {
	Account string `json:"account"`
	ChatID  string `json:"chatId"`
	Active  bool   `json:"active"`
}

// MediaRequest downloads a media item into Path (inside the plugin's
// account directory, chosen by the host).
type MediaRequest struct {
	Account string `json:"account"`
	ChatID  string `json:"chatId,omitempty"`
	Ref     string `json:"ref"`
	Path    string `json:"path"`
}

// MediaResult describes the downloaded file.
type MediaResult struct {
	MIMEType string `json:"mimeType,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size"`
}

// PairRequest starts linking a new account. The host chooses Account (the
// directory name of the new session).
type PairRequest struct {
	PairingID  string `json:"pairingId"`
	Account    string `json:"account"`
	Method     string `json:"method"`
	Phone      string `json:"phone,omitempty"`
	Homeserver string `json:"homeserver,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"`
	Token      string `json:"token,omitempty"`
	// Network selects the bridge network in bridge mode (whatsapp, signal ...)
	// and BridgeAccount the Matrix account that talks to the bridge bot.
	Network       string `json:"network,omitempty"`
	BridgeAccount string `json:"bridgeAccount,omitempty"`
}

// PairInput answers an input request of a pairing (Field "code",
// "password", "phone" ...).
type PairInput struct {
	PairingID string `json:"pairingId"`
	Field     string `json:"field"`
	Value     string `json:"value"`
}

// PairingRef addresses a running pairing.
type PairingRef struct {
	PairingID string `json:"pairingId"`
}

// Pairing event kinds.
const (
	PairEventQR      = "qr"      // QR holds the data to render as QR code
	PairEventCode    = "code"    // Code is a pairing code to type into the phone
	PairEventInput   = "input"   // Field is requested from the user
	PairEventInfo    = "info"    // progress message
	PairEventSuccess = "success" // Account is linked
	PairEventError   = "error"   // pairing failed (final)
)

// PairEvent is a pairing progress update.
type PairEvent struct {
	PairingID string `json:"pairingId"`
	Kind      string `json:"kind"`
	QR        string `json:"qr,omitempty"`
	Code      string `json:"code,omitempty"`
	Field     string `json:"field,omitempty"`
	Message   string `json:"message,omitempty"`
	// ErrorCode is the protocol error code of an error event (0 = unspecified).
	ErrorCode int `json:"errorCode,omitempty"`
	// ExpiresAt is set for QR codes and pairing codes.
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	Account   *Account  `json:"account,omitempty"`
}

// Event kinds.
const (
	EventMessage  = "message"  // Message (new or updated)
	EventEdit     = "edit"     // Message with new Text/EditedAt
	EventDelete   = "delete"   // ChatID + MessageID
	EventReaction = "reaction" // Reaction (Emoji empty = removed)
	EventReceipt  = "receipt"  // Receipt
	EventTyping   = "typing"   // Typing (ephemeral)
	EventChat     = "chat"     // Chat added or changed
	EventStatus   = "status"   // account state changed
	EventPairing  = "pairing"  // Pairing progress
)

// ReactionEvent changes a reaction of Sender on MessageID.
type ReactionEvent struct {
	ChatID    string  `json:"chatId"`
	MessageID string  `json:"messageId"`
	Sender    Contact `json:"sender"`
	FromMe    bool    `json:"fromMe,omitempty"`
	Emoji     string  `json:"emoji"` // empty: reaction removed
}

// Receipt kinds.
const (
	ReceiptDelivered = "delivered"
	ReceiptRead      = "read"
)

// Receipt reports delivery/read state of messages. FromMe marks the user's
// own read receipts from another device (the chat was read elsewhere).
type Receipt struct {
	ChatID     string    `json:"chatId"`
	MessageIDs []string  `json:"messageIds"`
	Kind       string    `json:"kind"`
	Time       time.Time `json:"time"`
	FromMe     bool      `json:"fromMe,omitempty"`
}

// Typing is an ephemeral typing notification.
type Typing struct {
	ChatID string  `json:"chatId"`
	Sender Contact `json:"sender"`
	Active bool    `json:"active"`
}

// AccountStatus reports a state change of an account.
type AccountStatus struct {
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	UpdateHint string `json:"updateHint,omitempty"`
}

// Event is pushed by the plugin as NotifyEvent notification.
type Event struct {
	Kind      string         `json:"kind"`
	Account   string         `json:"account,omitempty"`
	Chat      *Chat          `json:"chat,omitempty"`
	Message   *Message       `json:"message,omitempty"`
	ChatID    string         `json:"chatId,omitempty"`
	MessageID string         `json:"messageId,omitempty"`
	Reaction  *ReactionEvent `json:"reaction,omitempty"`
	Receipt   *Receipt       `json:"receipt,omitempty"`
	Typing    *Typing        `json:"typing,omitempty"`
	Status    *AccountStatus `json:"status,omitempty"`
	Pairing   *PairEvent     `json:"pairing,omitempty"`
}
