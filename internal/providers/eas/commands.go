// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// newClientID returns a random client id for SendMail / Sync Add.
func newClientID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ==== ItemOperations ===========================================================

// FetchMIME downloads the complete MIME source of a mail item with
// ItemOperations/Fetch (12.0+).
func (c *Client) FetchMIME(ctx context.Context, collectionID, serverID string) ([]byte, error) {
	opts := wbxml.E("ItemOperations:Options")
	if c.versionAtLeast(ctx, "14.0") {
		opts.Add(wbxml.S("AirSync:MIMESupport", "2"))
	}
	opts.Add(wbxml.E("AirSyncBase:BodyPreference", wbxml.S("AirSyncBase:Type", strconv.Itoa(BodyMIME))))
	req := wbxml.E("ItemOperations:ItemOperations", wbxml.E("ItemOperations:Fetch",
		wbxml.S("ItemOperations:Store", "Mailbox"), wbxml.S("AirSync:CollectionId", collectionID),
		wbxml.S("AirSync:ServerId", serverID), opts))
	resp, err := c.Do(ctx, "ItemOperations", req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("eas: ItemOperations: empty response")
	}
	if st := resp.IntOf("ItemOperations:Status", 1); st != 1 {
		return nil, &StatusError{Cmd: "ItemOperations", Status: st}
	}
	f := resp.Path("ItemOperations:Response/ItemOperations:Fetch")
	if f == nil {
		return nil, errors.New("eas: ItemOperations: no fetch result")
	}
	if st := f.IntOf("ItemOperations:Status", 1); st != 1 {
		e := &StatusError{Cmd: "ItemOperations", Status: st, Where: "Fetch"}
		if st == 6 || st == 15 || st == 8 {
			return nil, errors.Join(ErrItemNotFound, e)
		}
		return nil, e
	}
	props := f.Child("ItemOperations:Properties")
	if b := props.Child("AirSyncBase:Body"); b != nil {
		return b.Child("AirSyncBase:Data").Bytes(), nil
	}
	if m := props.Child("Email:MIMEData"); m != nil {
		return m.Bytes(), nil
	}
	return nil, errors.New("eas: ItemOperations: response has no MIME data")
}

// FetchAttachment downloads an attachment by its FileReference (12.0+).
func (c *Client) FetchAttachment(ctx context.Context, fileReference string) (data []byte, contentType string, err error) {
	req := wbxml.E("ItemOperations:ItemOperations", wbxml.E("ItemOperations:Fetch",
		wbxml.S("ItemOperations:Store", "Mailbox"), wbxml.S("AirSyncBase:FileReference", fileReference)))
	resp, err := c.Do(ctx, "ItemOperations", req)
	if err != nil {
		return nil, "", err
	}
	f := resp.Path("ItemOperations:Response/ItemOperations:Fetch")
	if st := f.IntOf("ItemOperations:Status", -1); st != 1 {
		return nil, "", &StatusError{Cmd: "ItemOperations", Status: st, Where: "Fetch"}
	}
	p := f.Child("ItemOperations:Properties")
	return p.Child("ItemOperations:Data").Bytes(), p.TextOf("AirSyncBase:ContentType"), nil
}

// ==== MoveItems ================================================================

// MoveResult is the outcome of moving one item.
type MoveResult struct {
	SrcMsgID string
	DstMsgID string
	Status   int
}

// MoveItems moves items to dstFolder ([MS-ASCMD] 2.2.1.12). Status 3 is
// success.
func (c *Client) MoveItems(ctx context.Context, srcFolder string, serverIDs []string, dstFolder string) ([]MoveResult, error) {
	req := wbxml.E("Move:MoveItems")
	for _, id := range serverIDs {
		req.Add(wbxml.E("Move:Move", wbxml.S("Move:SrcMsgId", id), wbxml.S("Move:SrcFldId", srcFolder),
			wbxml.S("Move:DstFldId", dstFolder)))
	}
	resp, err := c.Do(ctx, "MoveItems", req)
	if err != nil {
		return nil, err
	}
	byID := map[string]MoveResult{}
	for _, r := range resp.All("Move:Response") {
		mr := MoveResult{SrcMsgID: r.TextOf("Move:SrcMsgId"), DstMsgID: r.TextOf("Move:DstMsgId"), Status: r.IntOf("Move:Status", 0)}
		byID[mr.SrcMsgID] = mr
	}
	out := make([]MoveResult, len(serverIDs))
	for i, id := range serverIDs {
		mr, ok := byID[id]
		if !ok {
			mr = MoveResult{SrcMsgID: id, Status: 5}
		}
		out[i] = mr
	}
	return out, nil
}

// ==== SendMail / SmartReply / SmartForward ====================================

// SendOptions select SmartReply/SmartForward and Sent handling.
type SendOptions struct {
	// SaveInSent stores a copy in Sent Items (default true via SendMail).
	SaveInSent bool
	// Smart is "", "SmartReply" or "SmartForward".
	Smart string
	// SourceFolder/SourceItem identify the original for Smart*.
	SourceFolder, SourceItem string
	// ReplaceMime sends the MIME as is instead of letting the server
	// append the original (for Smart*).
	ReplaceMime bool
}

// SendMIME submits a complete MIME message. With 14.0+ the ComposeMail
// WBXML commands are used, otherwise the MIME is posted as message/rfc822.
func (c *Client) SendMIME(ctx context.Context, mime []byte, o SendOptions) error {
	cmd := "SendMail"
	if o.Smart != "" {
		if o.Smart != "SmartReply" && o.Smart != "SmartForward" {
			return fmt.Errorf("eas: unknown send command %q", o.Smart)
		}
		cmd = o.Smart
	}
	if c.versionAtLeast(ctx, "14.0") {
		n := wbxml.E("ComposeMail:"+cmd, wbxml.S("ComposeMail:ClientId", newClientID()))
		if o.Smart != "" {
			n.Add(wbxml.E("ComposeMail:Source", wbxml.S("ComposeMail:FolderId", o.SourceFolder),
				wbxml.S("ComposeMail:ItemId", o.SourceItem)))
		}
		if o.SaveInSent {
			n.Add(wbxml.E("ComposeMail:SaveInSentItems"))
		}
		if o.Smart != "" && o.ReplaceMime {
			n.Add(wbxml.E("ComposeMail:ReplaceMime"))
		}
		n.Add(wbxml.Op("ComposeMail:Mime", mime))
		resp, err := c.Do(ctx, cmd, n)
		if err != nil {
			return err
		}
		if st := resp.IntOf("ComposeMail:Status", 1); st != 1 {
			return &StatusError{Cmd: cmd, Status: st}
		}
		return nil
	}
	var params [][2]string
	if o.Smart != "" {
		params = append(params, [2]string{"ItemId", o.SourceItem}, [2]string{"CollectionId", o.SourceFolder})
	}
	if o.SaveInSent {
		params = append(params, [2]string{"SaveInSent", "T"})
	}
	_, err := c.do(ctx, &request{cmd: cmd, params: params, body: mime, contentType: "message/rfc822"})
	return err
}

// ==== MeetingResponse ==========================================================

// Meeting responses ([MS-ASCMD] 2.2.3.181).
const (
	MeetingAccept    = 1
	MeetingTentative = 2
	MeetingDecline   = 3
)

// MeetingResponse answers a meeting request (in a mail or calendar
// folder) and returns the calendar item id of the accepted meeting.
func (c *Client) MeetingResponse(ctx context.Context, response int, collectionID, requestID, instanceID string) (string, error) {
	r := wbxml.E("MeetingResponse:Request", wbxml.S("MeetingResponse:UserResponse", strconv.Itoa(response)),
		wbxml.S("MeetingResponse:CollectionId", collectionID), wbxml.S("MeetingResponse:RequestId", requestID))
	if instanceID != "" {
		r.Add(wbxml.S("MeetingResponse:InstanceId", instanceID))
	}
	resp, err := c.Do(ctx, "MeetingResponse", wbxml.E("MeetingResponse:MeetingResponse", r))
	if err != nil {
		return "", err
	}
	res := resp.Child("MeetingResponse:Result")
	if st := res.IntOf("MeetingResponse:Status", -1); st != 1 {
		return "", &StatusError{Cmd: "MeetingResponse", Status: st}
	}
	return res.TextOf("MeetingResponse:CalendarId"), nil
}

// ==== Search ===================================================================

// SearchResult is one hit of a mailbox search.
type SearchResult struct {
	CollectionID string
	ServerID     string
	LongID       string
	Class        string
	Properties   *wbxml.Node
}

// GALEntry is one hit of a global address list search.
type GALEntry struct {
	DisplayName, EmailAddress, FirstName, LastName, Company, Title, Office, Phone, MobilePhone, Alias string
}

func (c *Client) search(ctx context.Context, store *wbxml.Node) (*wbxml.Node, error) {
	resp, err := c.Do(ctx, "Search", wbxml.E("Search:Search", store))
	if err != nil {
		return nil, err
	}
	if st := resp.IntOf("Search:Status", 1); st != 1 {
		return nil, &StatusError{Cmd: "Search", Status: st}
	}
	s := resp.Path("Search:Response/Search:Store")
	if st := s.IntOf("Search:Status", 1); st != 1 {
		return nil, &StatusError{Cmd: "Search", Status: st, Where: "Store"}
	}
	return s, nil
}

// SearchMailbox runs a free-text search, optionally limited to one folder
// (with subfolders), and returns up to limit results.
func (c *Client) SearchMailbox(ctx context.Context, query, collectionID string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	and := wbxml.E("Search:And", wbxml.S("AirSync:Class", "Email"))
	if collectionID != "" {
		and.Add(wbxml.S("AirSync:CollectionId", collectionID))
	}
	and.Add(wbxml.S("Search:FreeText", query))
	opts := wbxml.E("Search:Options", wbxml.S("Search:Range", "0-"+strconv.Itoa(limit-1)), wbxml.E("Search:RebuildResults"))
	if collectionID != "" {
		opts.Add(wbxml.E("Search:DeepTraversal"))
	}
	opts.Add(wbxml.E("AirSyncBase:BodyPreference", wbxml.S("AirSyncBase:Type", "1"),
		wbxml.S("AirSyncBase:TruncationSize", "256")))
	s, err := c.search(ctx, wbxml.E("Search:Store", wbxml.S("Search:Name", "Mailbox"), wbxml.E("Search:Query", and), opts))
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range s.All("Search:Result") {
		if !r.Has("Search:Properties") {
			continue
		}
		out = append(out, SearchResult{CollectionID: r.TextOf("AirSync:CollectionId"), ServerID: r.TextOf("AirSync:ServerId"),
			LongID: r.TextOf("Search:LongId"), Class: r.TextOf("AirSync:Class"), Properties: r.Child("Search:Properties")})
	}
	return out, nil
}

// SearchGAL searches the global address list.
func (c *Client) SearchGAL(ctx context.Context, query string, limit int) ([]GALEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	s, err := c.search(ctx, wbxml.E("Search:Store", wbxml.S("Search:Name", "GAL"), wbxml.S("Search:Query", query),
		wbxml.E("Search:Options", wbxml.S("Search:Range", "0-"+strconv.Itoa(limit-1)))))
	if err != nil {
		return nil, err
	}
	var out []GALEntry
	for _, r := range s.All("Search:Result") {
		p := r.Child("Search:Properties")
		if p == nil {
			continue
		}
		out = append(out, GALEntry{
			DisplayName: p.TextOf("GAL:DisplayName"), EmailAddress: p.TextOf("GAL:EmailAddress"),
			FirstName: p.TextOf("GAL:FirstName"), LastName: p.TextOf("GAL:LastName"), Company: p.TextOf("GAL:Company"),
			Title: p.TextOf("GAL:Title"), Office: p.TextOf("GAL:Office"), Phone: p.TextOf("GAL:Phone"),
			MobilePhone: p.TextOf("GAL:MobilePhone"), Alias: p.TextOf("GAL:Alias"),
		})
	}
	return out, nil
}

// Recipient is a resolved recipient.
type Recipient struct {
	Query        string
	DisplayName  string
	EmailAddress string
	// Type is 1 for GAL entries and 2 for contacts.
	Type int
}

// ResolveRecipients resolves names or addresses against GAL and contacts.
func (c *Client) ResolveRecipients(ctx context.Context, names ...string) ([]Recipient, error) {
	req := wbxml.E("ResolveRecipients:ResolveRecipients")
	for _, n := range names {
		req.Add(wbxml.S("ResolveRecipients:To", n))
	}
	resp, err := c.Do(ctx, "ResolveRecipients", req)
	if err != nil {
		return nil, err
	}
	if st := resp.IntOf("ResolveRecipients:Status", 1); st != 1 {
		return nil, &StatusError{Cmd: "ResolveRecipients", Status: st}
	}
	var out []Recipient
	for _, r := range resp.All("ResolveRecipients:Response") {
		q := r.TextOf("ResolveRecipients:To")
		for _, rc := range r.All("ResolveRecipients:Recipient") {
			out = append(out, Recipient{Query: q, DisplayName: rc.TextOf("ResolveRecipients:DisplayName"),
				EmailAddress: rc.TextOf("ResolveRecipients:EmailAddress"), Type: rc.IntOf("ResolveRecipients:Type", 0)})
		}
	}
	return out, nil
}

// ==== Settings =================================================================

// OOF states ([MS-ASCMD] 2.2.3.118).
const (
	OofDisabled = 0
	OofGlobal   = 1
	OofTimed    = 2
)

// Oof are the out-of-office settings.
type Oof struct {
	State        int
	Start, End   time.Time
	InternalText string
	// ExternalText is sent to external senders (known and unknown);
	// ExternalEnabled switches it on.
	ExternalText    string
	ExternalEnabled bool
	// ExternalKnownOnly limits external replies to senders in contacts.
	ExternalKnownOnly bool
}

// easTime formats a time as EAS date-time ("2006-01-02T15:04:05.000Z").
func easTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// parseEASTime parses the date-time formats used by EAS.
func parseEASTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02T15:04:05.000Z", time.RFC3339Nano, "20060102T150405Z", "2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// GetOof reads the out-of-office settings.
func (c *Client) GetOof(ctx context.Context) (Oof, error) {
	resp, err := c.Do(ctx, "Settings", wbxml.E("Settings:Settings", wbxml.E("Settings:Oof",
		wbxml.E("Settings:Get", wbxml.S("Settings:BodyType", "Text")))))
	if err != nil {
		return Oof{}, err
	}
	if st := resp.IntOf("Settings:Status", 1); st != 1 {
		return Oof{}, &StatusError{Cmd: "Settings", Status: st}
	}
	oof := resp.Child("Settings:Oof")
	if st := oof.IntOf("Settings:Status", 1); st != 1 {
		return Oof{}, &StatusError{Cmd: "Settings", Status: st, Where: "Oof"}
	}
	g := oof.Child("Settings:Get")
	o := Oof{State: g.IntOf("Settings:OofState", 0), Start: parseEASTime(g.TextOf("Settings:StartTime")),
		End: parseEASTime(g.TextOf("Settings:EndTime"))}
	for _, m := range g.All("Settings:OofMessage") {
		enabled := m.IntOf("Settings:Enabled", 0) == 1
		text := m.TextOf("Settings:ReplyMessage")
		switch {
		case m.Has("Settings:AppliesToInternal"):
			o.InternalText = text
		case m.Has("Settings:AppliesToExternalUnknown"):
			if enabled {
				o.ExternalEnabled = true
				o.ExternalKnownOnly = false
			}
			if o.ExternalText == "" || enabled {
				o.ExternalText = text
			}
		case m.Has("Settings:AppliesToExternalKnown"):
			if enabled && !o.ExternalEnabled {
				o.ExternalEnabled, o.ExternalKnownOnly = true, true
			}
			if o.ExternalText == "" {
				o.ExternalText = text
			}
		}
	}
	return o, nil
}

// SetOof writes the out-of-office settings.
func (c *Client) SetOof(ctx context.Context, o Oof) error {
	set := wbxml.E("Settings:Set", wbxml.S("Settings:OofState", strconv.Itoa(o.State)))
	if o.State == OofTimed {
		set.Add(wbxml.S("Settings:StartTime", easTime(o.Start)), wbxml.S("Settings:EndTime", easTime(o.End)))
	}
	msg := func(applies string, enabled bool, text string) *wbxml.Node {
		m := wbxml.E("Settings:OofMessage", wbxml.E("Settings:"+applies), wbxml.S("Settings:Enabled", boolStr(enabled)))
		if text != "" {
			m.Add(wbxml.S("Settings:ReplyMessage", text), wbxml.S("Settings:BodyType", "Text"))
		}
		return m
	}
	on := o.State != OofDisabled
	set.Add(msg("AppliesToInternal", on && o.InternalText != "", o.InternalText),
		msg("AppliesToExternalKnown", on && o.ExternalEnabled, o.ExternalText),
		msg("AppliesToExternalUnknown", on && o.ExternalEnabled && !o.ExternalKnownOnly, o.ExternalText))
	resp, err := c.Do(ctx, "Settings", wbxml.E("Settings:Settings", wbxml.E("Settings:Oof", set)))
	if err != nil {
		return err
	}
	if st := resp.IntOf("Settings:Status", 1); st != 1 {
		return &StatusError{Cmd: "Settings", Status: st}
	}
	if st := resp.Child("Settings:Oof").IntOf("Settings:Status", 1); st != 1 {
		return &StatusError{Cmd: "Settings", Status: st, Where: "Oof"}
	}
	return nil
}

// SendDeviceInformation reports the device information (Settings command).
func (c *Client) SendDeviceInformation(ctx context.Context) error {
	resp, err := c.Do(ctx, "Settings", wbxml.E("Settings:Settings", c.deviceInformation()))
	if err != nil {
		return err
	}
	if st := resp.IntOf("Settings:Status", 1); st != 1 {
		return &StatusError{Cmd: "Settings", Status: st}
	}
	return nil
}

// UserInformation returns the SMTP addresses of the user (Settings).
func (c *Client) UserInformation(ctx context.Context) (primary string, addresses []string, err error) {
	resp, err := c.Do(ctx, "Settings", wbxml.E("Settings:Settings", wbxml.E("Settings:UserInformation", wbxml.E("Settings:Get"))))
	if err != nil {
		return "", nil, err
	}
	g := resp.Path("Settings:UserInformation/Settings:Get")
	addrNodes := g.Path("Settings:EmailAddresses")
	if acc := g.Path("Settings:Accounts/Settings:Account"); acc != nil { // 14.1+
		addrNodes = acc.Child("Settings:EmailAddresses")
	}
	for _, a := range addrNodes.Elems() {
		switch {
		case a.Is("Settings:PrimarySmtpAddress"):
			primary = a.Value()
			addresses = append(addresses, a.Value())
		case a.Is("Settings:SmtpAddress"):
			addresses = append(addresses, a.Value())
		}
	}
	seen := map[string]bool{}
	uniq := addresses[:0]
	for _, a := range addresses {
		if k := strings.ToLower(a); !seen[k] {
			seen[k] = true
			uniq = append(uniq, a)
		}
	}
	addresses = uniq
	if primary == "" && len(addresses) > 0 {
		primary = addresses[0]
	}
	return primary, addresses, nil
}
