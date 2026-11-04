// SPDX-License-Identifier: AGPL-3.0-or-later

package categorize

import (
	"context"
	"testing"
)

func h(kv ...string) map[string][]string {
	m := map[string][]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = append(m[kv[i]], kv[i+1])
	}
	return m
}

func TestFixtures(t *testing.T) {
	cases := []struct {
		name string
		m    Mail
		want string
	}{
		{"de newsletter", Mail{From: "newsletter@shop-beispiel.de", Subject: "Unsere Angebote der Woche: 20% Rabatt",
			Text: "Jetzt kaufen! Zum Abmelden hier klicken.", Headers: h("List-Id", "<news.shop-beispiel.de>", "List-Unsubscribe", "<https://shop-beispiel.de/u>", "Precedence", "bulk")}, Newsletter},
		{"en newsletter", Mail{From: "hello@weekly.dev", Subject: "This week in Go — edition 412",
			Text: "View in browser. Unsubscribe at any time.", Headers: h("List-Unsubscribe", "<mailto:u@weekly.dev>")}, Newsletter},
		{"de invoice", Mail{From: "rechnung@telekom.de", Subject: "Ihre Rechnung für Oktober 2026",
			Text: "Ihre Rechnung steht bereit. Der Betrag wird per Lastschrift eingezogen.", AttachmentNames: []string{"Rechnung_2026_10.pdf"}, AttachmentTypes: []string{"application/pdf"}}, Invoice},
		{"en order", Mail{From: "auto-confirm@amazon.com", Subject: "Your Order #123-456 has shipped",
			Text: "Thanks for your order. Order confirmation and receipt attached."}, Invoice},
		{"e-invoice", Mail{From: "buchhaltung@lieferant.de", Subject: "RE-2026-0042", IsInvoice: true}, Invoice},
		{"github notification", Mail{From: "notifications@github.com", Subject: "Re: [org/repo] Fix parser (PR #12)",
			Text: "@anna commented on this pull request.", Headers: h("List-Id", "org/repo <repo.org.github.com>", "List-Unsubscribe", "<mailto:x@github.com>")}, Notification},
		{"de security", Mail{From: "no-reply@accounts.google.com", Subject: "Sicherheitswarnung: Neue Anmeldung",
			Text: "Ihr Konto wurde auf einem neuen Gerät verwendet.", Headers: h("Auto-Submitted", "auto-generated")}, Notification},
		{"linkedin", Mail{From: "invitations@linkedin.com", Subject: "Anna Meier möchte sich mit Ihnen vernetzen",
			Text: "Connection request from Anna", Headers: h("List-Unsubscribe", "<https://linkedin.com/u>")}, Social},
		{"en social", Mail{From: "notification@facebookmail.com", Subject: "Bob mentioned you in a comment", Text: "Bob mentioned you"}, Social},
		{"invitation", Mail{From: "chef@firma.de", Subject: "Einladung: Quartalsplanung Do 14:00", HasCalendar: true,
			AttachmentNames: []string{"invite.ics"}, AttachmentTypes: []string{"text/calendar"}}, Appointment},
		{"en accepted", Mail{From: "bob@example.org", Subject: "Accepted: Project kickoff", HasCalendar: true}, Appointment},
		{"de personal", Mail{From: "oma@web.de", Subject: "Wochenende", Text: "Hallo Schatz, kommt ihr am Sonntag zum Kaffee? Liebe Grüße, Oma", KnownContact: true}, Personal},
		{"en personal", Mail{From: "carla@example.org", Subject: "Quick question", Text: "Hi Anna, do you have the slides from yesterday? Thanks, Carla"}, Personal},
	}
	c := &Classifier{}
	for _, tc := range cases {
		r, err := c.Classify(context.Background(), tc.m)
		if err != nil {
			t.Fatal(err)
		}
		if r.Category != tc.want {
			t.Errorf("%s: got %s (%v) want %s", tc.name, r.Category, r.Scores, tc.want)
		}
	}
}

func TestLearnMovesCategory(t *testing.T) {
	model := NewMemModel()
	c := &Classifier{Model: model}
	ctx := context.Background()
	m := Mail{From: "team@vereinsblatt.de", Subject: "Vereinsblatt Juni", Text: "Liebe Mitglieder, das Sommerfest findet statt"}
	r, _ := c.Classify(ctx, m)
	if r.Category != Personal {
		t.Fatalf("baseline: %v", r)
	}
	for i := 0; i < 4; i++ {
		_ = c.Learn(ctx, Mail{From: "team@vereinsblatt.de", Subject: "Vereinsblatt Ausgabe", Text: "Liebe Mitglieder, Termine und Neuigkeiten"}, "", Newsletter)
	}
	_ = c.Learn(ctx, Mail{From: "oma@web.de", Subject: "Kaffee", Text: "Hallo Schatz"}, "", Personal)
	r, _ = c.Classify(ctx, m)
	if r.Category != Newsletter || r.Source != "model" {
		t.Fatalf("after learning: %+v", r)
	}
}

type fixedRefiner struct{}

func (fixedRefiner) Refine(_ context.Context, _ Mail, r Result) (Result, error) {
	r.Category = "termine"
	r.Source = ""
	return r, nil
}

func TestRefinerAndAliases(t *testing.T) {
	c := &Classifier{Refiner: fixedRefiner{}, RefineBelow: 1.01}
	r, _ := c.Classify(context.Background(), Mail{From: "x@y.de", Subject: "?"})
	if r.Category != Appointment || r.Source != "ai" {
		t.Fatalf("refiner: %+v", r)
	}
	if Normalize("Rechnung") != Invoice || Normalize("Social") != Social || Normalize("x") != "" {
		t.Fatal("aliases")
	}
}
