// SPDX-License-Identifier: AGPL-3.0-or-later

package eas

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/Midas1989/Mailclient/internal/providers/eas/wbxml"
)

// Ping status codes ([MS-ASCMD] 2.2.3.177.10).
const (
	PingExpired          = 1
	PingChanges          = 2
	PingMissingParams    = 3
	PingSyntaxError      = 4
	PingBadHeartbeat     = 5
	PingTooManyFolders   = 6
	PingHierarchyChanged = 7
	PingServerError      = 8
)

// PingFolder is a folder monitored by Ping.
type PingFolder struct {
	ID    string
	Class string // "Email", "Calendar", "Contacts", "Tasks"
}

// PingResult is the answer of a Ping.
type PingResult struct {
	Status  int
	Changed []string // folder ids with changes (status 2)
	// Heartbeat is the allowed heartbeat for status 5 (seconds).
	Heartbeat int
	// MaxFolders is the folder limit for status 6.
	MaxFolders int
}

// Ping waits up to heartbeat seconds for changes in folders
// ([MS-ASCMD] 2.2.1.13). The request blocks on the server; it is the
// long-poll push mechanism of EAS.
func (c *Client) Ping(ctx context.Context, heartbeat time.Duration, folders []PingFolder) (PingResult, error) {
	secs := int(heartbeat / time.Second)
	fs := wbxml.E("Ping:Folders")
	for _, f := range folders {
		fs.Add(wbxml.E("Ping:Folder", wbxml.S("Ping:Id", f.ID), wbxml.S("Ping:Class", f.Class)))
	}
	data, err := wbxml.Marshal(wbxml.E("Ping:Ping", wbxml.S("Ping:HeartbeatInterval", strconv.Itoa(secs)), fs))
	if err != nil {
		return PingResult{}, err
	}
	resp, err := c.do(ctx, &request{cmd: "Ping", body: data, contentType: wbxml.ContentType,
		timeout: heartbeat + 90*time.Second})
	if err != nil {
		return PingResult{}, err
	}
	if resp == nil {
		return PingResult{}, errors.New("eas: Ping: empty response")
	}
	r := PingResult{Status: resp.IntOf("Ping:Status", 0), Heartbeat: resp.IntOf("Ping:HeartbeatInterval", 0),
		MaxFolders: resp.IntOf("Ping:MaxFolders", 0)}
	for _, f := range resp.Path("Ping:Folders").All("Ping:Folder") {
		r.Changed = append(r.Changed, f.Value())
	}
	return r, nil
}
