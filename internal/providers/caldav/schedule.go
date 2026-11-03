// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Midas1989/Mailclient/internal/pim/ical"
	"github.com/Midas1989/Mailclient/internal/providers/dav"
)

type scheduleResponse struct {
	Responses []struct {
		Recipient struct {
			Href string `xml:"DAV: href"`
		} `xml:"urn:ietf:params:xml:ns:caldav recipient"`
		Status       string `xml:"urn:ietf:params:xml:ns:caldav request-status"`
		CalendarData string `xml:"urn:ietf:params:xml:ns:caldav calendar-data"`
	} `xml:"urn:ietf:params:xml:ns:caldav response"`
}

func parseScheduleResponse(b []byte) (map[string][]Period, error) {
	var sr scheduleResponse
	if err := xml.Unmarshal(b, &sr); err != nil {
		return nil, fmt.Errorf("caldav: invalid schedule-response: %w", err)
	}
	out := map[string][]Period{}
	for _, r := range sr.Responses {
		if !strings.HasPrefix(strings.TrimSpace(r.Status), "2") {
			continue
		}
		cal, err := ical.Parse(r.CalendarData)
		if err != nil {
			continue
		}
		out[ical.MailAddress(r.Recipient.Href)] = ParsePeriods(cal)
	}
	return out, nil
}

// MaxICSSize bounds the size of subscribed ICS files.
const MaxICSSize = 32 << 20

// FetchICS downloads a subscribed calendar (http, https or webcal URL).
// etag is sent as If-None-Match; notModified reports a 304 answer.
func FetchICS(ctx context.Context, hc *http.Client, rawURL, etag string) (data []byte, newETag string, notModified bool, err error) {
	u := strings.TrimSpace(rawURL)
	switch {
	case strings.HasPrefix(strings.ToLower(u), "webcals://"):
		u = "https://" + u[len("webcals://"):]
	case strings.HasPrefix(strings.ToLower(u), "webcal://"):
		u = "https://" + u[len("webcal://"):]
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return nil, "", false, fmt.Errorf("caldav: unsupported subscription URL %q", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "text/calendar, */*;q=0.5")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, &dav.HTTPError{Method: "GET", URL: u, Status: resp.StatusCode}
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, MaxICSSize+1))
	if err != nil {
		return nil, "", false, err
	}
	if len(data) > MaxICSSize {
		return nil, "", false, fmt.Errorf("caldav: subscription larger than %d MB", MaxICSSize>>20)
	}
	return data, resp.Header.Get("ETag"), false, nil
}
