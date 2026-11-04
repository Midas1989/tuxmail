// SPDX-License-Identifier: AGPL-3.0-or-later

package spam

import (
	"context"
	"fmt"
	"slices"
	"testing"
)

var spamMails = []Message{
	{From: "winner@lotto-prize.biz", Subject: "You WON the LOTTERY!!", Text: "Claim your prize now, click http://lotto-prize.biz/claim free money casino"},
	{From: "offer@cheap-meds.ru", Subject: "Cheap pills online", Text: "Buy viagra cheap pills discount pharmacy http://cheap-meds.ru/buy"},
	{From: "noreply@crypto-gain.io", Subject: "Double your bitcoin", Text: "Invest in crypto now and double your bitcoin, guaranteed profit http://crypto-gain.io"},
	{From: "promo@casino-royal.biz", Subject: "Free casino bonus", Text: "Free spins casino bonus click here to claim your money http://casino-royal.biz"},
	{From: "info@prize-center.biz", Subject: "Congratulations winner", Text: "You are the lucky winner of our prize draw, claim money now http://prize-center.biz"},
	{From: "deals@pills-shop.ru", Subject: "Discount pharmacy", Text: "Order cheap pills without prescription, viagra discount http://pills-shop.ru"},
}

var hamMails = []Message{
	{From: "anna@firma.de", Subject: "Projekt Besprechung morgen", Text: "Hallo, wollen wir morgen um 10 Uhr das Projekt besprechen? Viele Grüße Anna"},
	{From: "chef@firma.de", Subject: "Quartalsbericht", Text: "Bitte schick mir den Quartalsbericht bis Freitag. Danke"},
	{From: "bob@example.org", Subject: "Lunch on Friday?", Text: "Hi, are we still on for lunch on Friday? Cheers, Bob"},
	{From: "team@firma.de", Subject: "Release notes", Text: "The release is scheduled for next week, please review the notes in the project wiki"},
	{From: "anna@firma.de", Subject: "Re: Projekt", Text: "Danke für die Unterlagen, ich melde mich morgen zum Projekt"},
	{From: "carla@example.org", Subject: "Meeting notes", Text: "Attached are the meeting notes from today, let me know if anything is missing"},
}

func trained(t *testing.T) *Classifier {
	t.Helper()
	c := &Classifier{Store: NewMemStore()}
	ctx := context.Background()
	for _, m := range spamMails {
		if err := c.Train(ctx, Tokenize(m), true); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range hamMails {
		if err := c.Train(ctx, Tokenize(m), false); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestBayesClassification(t *testing.T) {
	c := trained(t)
	ctx := context.Background()
	v, err := c.Classify(ctx, Message{From: "x@lotto-win.biz", Subject: "Claim your prize", Text: "lucky winner claim free money casino bonus http://prize-center.biz"})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Trained || v.Level != LevelSpam || !v.IsSpam {
		t.Fatalf("spam not detected: %+v", v)
	}
	v, _ = c.Classify(ctx, Message{From: "anna@firma.de", Subject: "Projekt", Text: "Hallo, können wir das Projekt morgen besprechen? Grüße"})
	if v.Level != LevelHam || v.Score > 0.3 {
		t.Fatalf("ham misclassified: %+v", v)
	}
}

func TestUntrainAndMinTraining(t *testing.T) {
	c := &Classifier{Store: NewMemStore()}
	ctx := context.Background()
	v, _ := c.Classify(ctx, spamMails[0])
	if v.Trained || v.Level != LevelHam {
		t.Fatalf("untrained model must be neutral: %+v", v)
	}
	toks := Tokenize(spamMails[0])
	_ = c.Train(ctx, toks, true)
	_ = c.Untrain(ctx, toks, true)
	s, h, _ := c.Store.SpamTotals(ctx)
	if s != 0 || h != 0 {
		t.Fatalf("totals after untrain: %d %d", s, h)
	}
}

func TestServerScores(t *testing.T) {
	m := Message{Headers: map[string][]string{
		"X-Spam-Status":          {"Yes, score=12.4 required=5.0 tests=BAYES_99,URIBL_BLACK autolearn=no"},
		"Authentication-Results": {"mx.example.org; spf=fail smtp.mailfrom=x.biz; dkim=none; dmarc=fail header.from=x.biz"},
	}}
	sv := ParseServer(m)
	if !sv.Present || !sv.Flagged || sv.Score != 12.4 || sv.Required != 5 || len(sv.Auth) != 3 {
		t.Fatalf("server verdict: %+v", sv)
	}
	v, _ := (&Classifier{}).Classify(context.Background(), m)
	if !v.IsSpam {
		t.Fatalf("server spam not honoured: %+v", v)
	}
	r := ParseServer(Message{Headers: map[string][]string{"X-Rspamd-Score": {"-1.2 / 15.0"}, "Authentication-Results": {"mx; spf=pass; dkim=pass; dmarc=pass"}}})
	if !r.HasRspamd || r.RspamdScore != -1.2 {
		t.Fatalf("rspamd: %+v", r)
	}
	v, _ = (&Classifier{}).ClassifyTokens(context.Background(), nil, r)
	if v.Level != LevelHam {
		t.Fatalf("clean rspamd mail: %+v", v)
	}
}

func TestTokenize(t *testing.T) {
	toks := Tokenize(Message{From: "News@Mail.Shop.co.uk", Subject: "BIG SALE TODAY!!", Text: "see https://track.shop.co.uk/x?id=1 and http://1.2.3.4/a",
		Headers: map[string][]string{"List-Unsubscribe": {"<mailto:u@x>"}, "Authentication-Results": {"mx; spf=pass"}}})
	for _, want := range []string{"from:shop.co.uk", "url:shop.co.uk", "url:ip-host", "url:plain-http", "s:shouting", "s:exclamations", "h:list-unsubscribe", "auth:spf=pass", "s:sale", "see"} {
		if !slices.Contains(toks, want) {
			t.Errorf("missing token %q in %v", want, toks)
		}
	}
	if Hash("a") == Hash("b") || Hash("a") < 0 {
		t.Fatal("hash")
	}
	_ = fmt.Sprint(toks)
}
