// SPDX-License-Identifier: AGPL-3.0-or-later

package meeting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// NextcloudCredentials resolves the Nextcloud account used for Talk. It is
// implemented by the cloud integration (the Nextcloud account's base URL,
// login name and app password from the secret store).
type NextcloudCredentials interface {
	NextcloudLogin(ctx context.Context, accountID int64) (baseURL, user, appPassword string, err error)
}

// TalkClient talks to the Nextcloud Talk (spreed) OCS API v4.
type TalkClient struct {
	Credentials NextcloudCredentials
	HTTP        *http.Client // default http.DefaultClient
}

// Talk conversation types.
const (
	talkRoomGroup  = 2
	talkRoomPublic = 3
)

type ocsResponse struct {
	OCS struct {
		Meta struct {
			Status     string `json:"status"`
			StatusCode int    `json:"statuscode"`
			Message    string `json:"message"`
		} `json:"meta"`
		Data json.RawMessage `json:"data"`
	} `json:"ocs"`
}

func (c *TalkClient) do(ctx context.Context, method, base, user, pass, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+"/ocs/v2.php/apps/spreed/api/v4"+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("meeting: talk %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var o ocsResponse
	_ = json.Unmarshal(raw, &o)
	if resp.StatusCode >= 300 {
		msg := o.OCS.Meta.Message
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("meeting: talk %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out != nil && len(o.OCS.Data) > 0 {
		return json.Unmarshal(o.OCS.Data, out)
	}
	return nil
}

// CreateRoom creates a Talk conversation, sets the password (public rooms)
// and invites the participants as e-mail guests.
func (c *TalkClient) CreateRoom(ctx context.Context, req Request) (Link, error) {
	if c.Credentials == nil {
		return Link{}, fmt.Errorf("%w: nextcloud credentials", ErrNotConfigured)
	}
	base, user, pass, err := c.Credentials.NextcloudLogin(ctx, req.AccountID)
	if err != nil {
		return Link{}, err
	}
	name := req.Title
	if name == "" {
		name = "TuxMail"
	}
	typ := talkRoomGroup
	if req.Public || len(req.Participants) > 0 || req.Password != "" {
		// External participants need a public (link) conversation.
		typ = talkRoomPublic
	}
	var room struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	if err := c.do(ctx, http.MethodPost, base, user, pass, "/room", url.Values{"roomType": {fmt.Sprint(typ)}, "roomName": {name}}, &room); err != nil {
		return Link{}, err
	}
	if room.Token == "" {
		return Link{}, fmt.Errorf("meeting: talk returned no room token")
	}
	l := Link{Provider: ProviderNextcloud, URL: strings.TrimRight(base, "/") + "/call/" + room.Token, Label: "Nextcloud Talk", Token: room.Token}
	if typ == talkRoomPublic {
		pw := req.Password
		if pw == "" {
			pw = RandomPassword()
		}
		if err := c.do(ctx, http.MethodPut, base, user, pass, "/room/"+room.Token+"/password", url.Values{"password": {pw}}, nil); err != nil {
			return l, err
		}
		l.Password = pw
	}
	for _, p := range req.Participants {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		src := "users"
		if strings.Contains(p, "@") {
			src = "emails"
		}
		if err := c.do(ctx, http.MethodPost, base, user, pass, "/room/"+room.Token+"/participants",
			url.Values{"newParticipant": {p}, "source": {src}}, nil); err != nil {
			return l, err
		}
	}
	return l, nil
}
