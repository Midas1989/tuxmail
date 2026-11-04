// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"testing"
	"time"

	"github.com/Midas1989/Mailclient/internal/events"
)

func TestComfortConditionFields(t *testing.T) {
	due := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	ev := events.Event{Type: events.MailReceived, Mail: &events.Mail{Subject: "Rechnung", SpamScore: 12, Category: "invoice",
		Invoice: &events.Invoice{Format: "facturx", Number: "RE-1", Amount: 119, Currency: "EUR", Seller: "Lieferant", Due: due}}}
	now := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	for src, want := range map[string]bool{
		`mail.spamScore < 50 && !mail.isSpam`:                              true,
		`mail.isInvoice && mail.invoice.present`:                           true,
		`mail.invoice.amount > 100 && mail.invoice.currency == "EUR"`:      true,
		`mail.invoice.due > time.now`:                                      true,
		`mail.invoice.seller == "Lieferant" && mail.category == "invoice"`: true,
		`mail.isSpam`: false,
	} {
		p, err := CompileExpr(src)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		got, err := p.Eval(NewEnv(ev, now, nil))
		if err != nil || got != want {
			t.Errorf("%s = %v, %v; want %v", src, got, err, want)
		}
	}
	if _, ok := ruleFields["mail.spamScore"]; !ok {
		t.Error("mail.spamScore not a rule field")
	}
}
