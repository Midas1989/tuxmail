// SPDX-License-Identifier: AGPL-3.0-or-later

package meeting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestJitsi(t *testing.T) {
	s := &Service{JitsiBaseURL: "https://jitsi.firma.de/"}
	l, err := s.Create(context.Background(), Request{Provider: ProviderJitsi, Title: "Projekt-Übergabe"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(l.URL, "https://jitsi.firma.de/ProjektUebergabe-") || len(l.Token) != len("ProjektUebergabe-")+16 {
		t.Fatalf("%+v", l)
	}
	l2, _ := s.Jitsi(Request{Title: "Projekt-Übergabe"})
	if l2.URL == l.URL {
		t.Fatal("room names must be random")
	}
	if _, err := s.Create(context.Background(), Request{Provider: ProviderTeams}); err == nil {
		t.Fatal("teams without creator")
	}
}

type creds struct{ base string }

func (c creds) NextcloudLogin(context.Context, int64) (string, string, string, error) {
	return c.base, "anna", "app-pass", nil
}

func TestTalk(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		u, p, _ := r.BasicAuth()
		if u != "anna" || p != "app-pass" || r.Header.Get("OCS-APIRequest") != "true" {
			w.WriteHeader(401)
			return
		}
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+" "+r.Form.Encode())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/ocs/v2.php/apps/spreed/api/v4/room" {
			w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{"token":"abc123","name":"Kickoff"}}}`))
			return
		}
		w.Write([]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200},"data":[]}}`))
	}))
	defer srv.Close()
	s := &Service{Talk: &TalkClient{Credentials: creds{srv.URL}}}
	l, err := s.Create(context.Background(), Request{Provider: ProviderNextcloud, Title: "Kickoff", Participants: []string{"bob@example.org"}})
	if err != nil {
		t.Fatal(err)
	}
	if l.URL != srv.URL+"/call/abc123" || l.Password == "" {
		t.Fatalf("%+v", l)
	}
	want := []string{
		"POST /ocs/v2.php/apps/spreed/api/v4/room roomName=Kickoff&roomType=3",
		"PUT /ocs/v2.php/apps/spreed/api/v4/room/abc123/password password=" + l.Password,
		"POST /ocs/v2.php/apps/spreed/api/v4/room/abc123/participants newParticipant=bob%40example.org&source=emails",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(calls, "\n"))
	}
}

func TestApplyToICS(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:1\r\nSUMMARY:Kickoff\r\nLOCATION:\r\nDESCRIPTION:Agenda folgt\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	l := Link{Provider: ProviderJitsi, URL: "https://meet.jit.si/Kickoff-abc", Label: "Jitsi Meet"}
	out := ApplyToICS(ics, l, "de")
	for _, want := range []string{"LOCATION:https://meet.jit.si/Kickoff-abc\r\n", "CONFERENCE;VALUE=URI;FEATURE=AUDIO,VIDEO;LABEL=Jitsi Meet:https://meet.jit.si/Kickoff-abc",
		`DESCRIPTION:Agenda folgt\n\nOnline-Meeting beitreten (Jitsi Meet): https://meet.jit.si/Kickoff-abc`} {
		if !strings.Contains(strings.ReplaceAll(out, "\r\n ", ""), want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if again := ApplyToICS(out, l, "de"); strings.Count(again, "CONFERENCE") != 1 {
		t.Fatalf("not idempotent:\n%s", again)
	}
}
