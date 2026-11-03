// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"errors"
	"strconv"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// Sync status codes ([MS-ASCMD] 2.2.3.177.16).
const (
	SyncStatusOK               = 1
	SyncStatusInvalidKey       = 3
	SyncStatusProtocolError    = 4
	SyncStatusServerError      = 5
	SyncStatusConversion       = 6
	SyncStatusConflict         = 7
	SyncStatusNotFound         = 8
	SyncStatusOutOfDisk        = 9
	SyncStatusHierarchyChanged = 12
	SyncStatusIncomplete       = 13
	SyncStatusRetry            = 16
)

// ErrInvalidSyncKey is returned when the server rejects a collection's
// sync key (status 3); the collection must be resynchronized from 0.
var ErrInvalidSyncKey = errors.New("eas: invalid sync key")

// ErrHierarchyChanged is returned when a FolderSync is required (status 12).
var ErrHierarchyChanged = errors.New("eas: folder hierarchy changed")

// Body types of AirSyncBase:Type.
const (
	BodyPlain = 1
	BodyHTML  = 2
	BodyRTF   = 3
	BodyMIME  = 4
)

// BodyPref is an AirSyncBase:BodyPreference.
type BodyPref struct {
	Type           int
	TruncationSize int // 0 = no truncation
	AllOrNone      bool
	Preview        int // 14.0+: characters of preview text (0 = none)
}

// SyncOptions are the per-collection options of a Sync request.
type SyncOptions struct {
	// Class is "Email", "Calendar", "Contacts", "Tasks" or "Notes".
	Class string
	// FilterType limits the time window ("0" = all, see [MS-ASCMD]
	// 2.2.3.68.1; "" = server default).
	FilterType string
	// WindowSize is the maximum number of changes per response (default 100).
	WindowSize int
	// BodyPreferences for 12.0+.
	BodyPreferences []BodyPref
	// MIMESupport 0/1/2 and, for EAS 2.5, the MIMETruncation code
	// (1-8; 0 = omit, 8 = no truncation).
	MIMESupport    int
	MIMETruncation int
	// ConversationMode requests whole conversations (14.0+).
	ConversationMode bool
	// DeletesAsMoves makes deletions move to Deleted Items (nil = omit).
	DeletesAsMoves *bool
	// NoChanges sends GetChanges=0 (for change-only requests).
	NoChanges bool
}

// ClientChange is a local change sent in Sync/Commands.
type ClientChange struct {
	// Kind is "Add", "Change", "Delete" or "Fetch".
	Kind     string
	ServerID string // Change, Delete, Fetch
	ClientID string // Add
	// Data are the ApplicationData children (Add, Change).
	Data []*wbxml.Node
}

// ServerItem is an item reported by the server.
type ServerItem struct {
	ServerID string
	Class    string
	Data     *wbxml.Node // AirSync:ApplicationData (nil for deletes)
}

// ItemResponse is the server's answer to one ClientChange.
type ItemResponse struct {
	Kind     string
	ServerID string
	ClientID string
	Status   int
	Data     *wbxml.Node
}

// SyncResult is the decoded answer for one collection.
type SyncResult struct {
	SyncKey       string
	MoreAvailable bool
	Added         []ServerItem
	Changed       []ServerItem
	Deleted       []string // includes SoftDelete
	SoftDeleted   []string
	Responses     []ItemResponse
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// SyncCollection runs one Sync request for a single collection. A sync key
// of "" or "0" starts the initial synchronization (options and changes are
// not sent in that case, as required by [MS-ASCMD] 3.1.5.7).
func (c *Client) SyncCollection(ctx context.Context, collectionID, syncKey string, opts SyncOptions, changes []ClientChange) (*SyncResult, error) {
	if syncKey == "" {
		syncKey = "0"
	}
	v12, v121, v14 := c.versionAtLeast(ctx, "12.0"), c.versionAtLeast(ctx, "12.1"), c.versionAtLeast(ctx, "14.0")
	col := wbxml.E("AirSync:Collection")
	if !v121 && opts.Class != "" {
		col.Add(wbxml.S("AirSync:Class", opts.Class))
	}
	col.Add(wbxml.S("AirSync:SyncKey", syncKey), wbxml.S("AirSync:CollectionId", collectionID))
	if syncKey != "0" {
		if opts.DeletesAsMoves != nil {
			col.Add(wbxml.S("AirSync:DeletesAsMoves", boolStr(*opts.DeletesAsMoves)))
		}
		if opts.NoChanges {
			col.Add(wbxml.S("AirSync:GetChanges", "0"))
		} else if !v121 || len(changes) > 0 {
			// GetChanges defaults to 1 for 12.1+; older versions and
			// requests with commands need it explicitly.
			col.Add(wbxml.S("AirSync:GetChanges", "1"))
		}
		ws := opts.WindowSize
		if ws <= 0 {
			ws = 100
		}
		col.Add(wbxml.S("AirSync:WindowSize", strconv.Itoa(ws)))
		if opts.ConversationMode && v14 {
			col.Add(wbxml.S("AirSync:ConversationMode", "1"))
		}
		o := wbxml.E("AirSync:Options")
		if opts.FilterType != "" {
			o.Add(wbxml.S("AirSync:FilterType", opts.FilterType))
		}
		if opts.MIMESupport > 0 {
			o.Add(wbxml.S("AirSync:MIMESupport", strconv.Itoa(opts.MIMESupport)))
			if !v12 && opts.MIMETruncation > 0 {
				o.Add(wbxml.S("AirSync:MIMETruncation", strconv.Itoa(opts.MIMETruncation)))
			}
		}
		if v12 {
			for _, bp := range opts.BodyPreferences {
				o.Add(bodyPreference(bp, v14))
			}
		}
		if len(o.Children) > 0 {
			col.Add(o)
		}
		if len(changes) > 0 {
			cmds := wbxml.E("AirSync:Commands")
			for _, ch := range changes {
				cmds.Add(changeNode(ch))
			}
			col.Add(cmds)
		}
	}
	resp, err := c.Do(ctx, "Sync", wbxml.E("AirSync:Sync", wbxml.E("AirSync:Collections", col)))
	if err != nil {
		return nil, err
	}
	res := &SyncResult{SyncKey: syncKey}
	if resp == nil {
		return res, nil // empty response: no changes
	}
	if st := resp.IntOf("AirSync:Status", SyncStatusOK); st != SyncStatusOK {
		return nil, syncStatusError(st, "")
	}
	for _, cn := range resp.Path("AirSync:Collections").All("AirSync:Collection") {
		if id := cn.TextOf("AirSync:CollectionId"); id != "" && id != collectionID {
			continue
		}
		if st := cn.IntOf("AirSync:Status", SyncStatusOK); st != SyncStatusOK {
			return nil, syncStatusError(st, "Collection")
		}
		if k := cn.TextOf("AirSync:SyncKey"); k != "" {
			res.SyncKey = k
		}
		res.MoreAvailable = cn.Has("AirSync:MoreAvailable")
		class := cn.TextOf("AirSync:Class")
		for _, cmd := range cn.Path("AirSync:Commands").Elems() {
			item := ServerItem{ServerID: cmd.TextOf("AirSync:ServerId"), Class: class, Data: cmd.Child("AirSync:ApplicationData")}
			if c := cmd.TextOf("AirSync:Class"); c != "" {
				item.Class = c
			}
			switch {
			case cmd.Is("AirSync:Add"):
				res.Added = append(res.Added, item)
			case cmd.Is("AirSync:Change"):
				res.Changed = append(res.Changed, item)
			case cmd.Is("AirSync:Delete"):
				res.Deleted = append(res.Deleted, item.ServerID)
			case cmd.Is("AirSync:SoftDelete"):
				res.Deleted = append(res.Deleted, item.ServerID)
				res.SoftDeleted = append(res.SoftDeleted, item.ServerID)
			}
		}
		for _, r := range cn.Path("AirSync:Responses").Elems() {
			res.Responses = append(res.Responses, ItemResponse{
				Kind: r.Tag.Name(), ServerID: r.TextOf("AirSync:ServerId"), ClientID: r.TextOf("AirSync:ClientId"),
				Status: r.IntOf("AirSync:Status", SyncStatusOK), Data: r.Child("AirSync:ApplicationData"),
			})
		}
	}
	return res, nil
}

func syncStatusError(st int, where string) error {
	e := &StatusError{Cmd: "Sync", Status: st, Where: where}
	switch st {
	case SyncStatusInvalidKey:
		return errors.Join(ErrInvalidSyncKey, e)
	case SyncStatusHierarchyChanged:
		return errors.Join(ErrHierarchyChanged, e)
	case SyncStatusNotFound:
		return errors.Join(ErrItemNotFound, e)
	}
	return e
}

func bodyPreference(bp BodyPref, v14 bool) *wbxml.Node {
	n := wbxml.E("AirSyncBase:BodyPreference", wbxml.S("AirSyncBase:Type", strconv.Itoa(bp.Type)))
	if bp.TruncationSize > 0 {
		n.Add(wbxml.S("AirSyncBase:TruncationSize", strconv.Itoa(bp.TruncationSize)))
	}
	if bp.AllOrNone {
		n.Add(wbxml.S("AirSyncBase:AllOrNone", "1"))
	}
	if bp.Preview > 0 && v14 {
		n.Add(wbxml.S("AirSyncBase:Preview", strconv.Itoa(bp.Preview)))
	}
	return n
}

func changeNode(ch ClientChange) *wbxml.Node {
	n := wbxml.E("AirSync:" + ch.Kind)
	switch ch.Kind {
	case "Add":
		n.Add(wbxml.S("AirSync:ClientId", ch.ClientID))
	default:
		n.Add(wbxml.S("AirSync:ServerId", ch.ServerID))
	}
	if ch.Kind == "Add" || ch.Kind == "Change" {
		n.Add(wbxml.E("AirSync:ApplicationData", ch.Data...))
	}
	return n
}

// ItemStatusError converts a failed ItemResponse into an error (nil on success).
func ItemStatusError(r ItemResponse) error {
	if r.Status == SyncStatusOK {
		return nil
	}
	return syncStatusError(r.Status, r.Kind)
}

// GetItemEstimate returns the number of items that a Sync with the given
// key would report.
func (c *Client) GetItemEstimate(ctx context.Context, collectionID, syncKey string, opts SyncOptions) (int, error) {
	var col *wbxml.Node
	switch {
	case c.versionAtLeast(ctx, "14.0"):
		o := wbxml.E("AirSync:Options")
		if opts.Class != "" {
			o.Add(wbxml.S("AirSync:Class", opts.Class))
		}
		if opts.FilterType != "" {
			o.Add(wbxml.S("AirSync:FilterType", opts.FilterType))
		}
		col = wbxml.E("ItemEstimate:Collection", wbxml.S("AirSync:SyncKey", syncKey),
			wbxml.S("ItemEstimate:CollectionId", collectionID))
		if len(o.Children) > 0 {
			col.Add(o)
		}
	default:
		col = wbxml.E("ItemEstimate:Collection")
		if opts.Class != "" {
			col.Add(wbxml.S("ItemEstimate:Class", opts.Class))
		}
		col.Add(wbxml.S("ItemEstimate:CollectionId", collectionID))
		if opts.FilterType != "" {
			col.Add(wbxml.S("AirSync:FilterType", opts.FilterType))
		}
		col.Add(wbxml.S("AirSync:SyncKey", syncKey))
	}
	resp, err := c.Do(ctx, "GetItemEstimate", wbxml.E("ItemEstimate:GetItemEstimate", wbxml.E("ItemEstimate:Collections", col)))
	if err != nil {
		return 0, err
	}
	r := resp.Child("ItemEstimate:Response")
	if r == nil {
		return 0, errors.New("eas: GetItemEstimate: no response")
	}
	if st := r.IntOf("ItemEstimate:Status", 1); st != 1 {
		e := &StatusError{Cmd: "GetItemEstimate", Status: st}
		if st == 4 {
			return 0, errors.Join(ErrInvalidSyncKey, e)
		}
		return 0, e
	}
	return r.Path("ItemEstimate:Collection").IntOf("ItemEstimate:Estimate", 0), nil
}
