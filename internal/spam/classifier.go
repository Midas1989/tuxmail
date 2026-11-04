// SPDX-License-Identifier: AGPL-3.0-or-later

package spam

import (
	"context"
	"math"
	"sort"
)

// Count is the training count of one token.
type Count struct {
	Spam int
	Ham  int
}

// TokenStore persists the token counts of one user (implemented by
// *store.Store, see store/comfort_spam.go). Tokens are passed as Hash values.
type TokenStore interface {
	SpamTotals(ctx context.Context) (spam, ham int, err error)
	SpamCounts(ctx context.Context, hashes []int64) (map[int64]Count, error)
	// SpamTrain adds delta (+1 or -1) to the spam or ham count of every
	// token and to the matching total; counts never become negative.
	SpamTrain(ctx context.Context, hashes []int64, spam bool, delta int) error
}

// Level is the decision.
type Level string

const (
	LevelHam    Level = "ham"
	LevelUnsure Level = "unsure"
	LevelSpam   Level = "spam"
)

// Thresholds on the combined score (0..1).
type Thresholds struct {
	Unsure float64 // >= Unsure: "unsure" (default 0.5)
	Spam   float64 // >= Spam: spam (default 0.9)
}

// DefaultThresholds are used for zero values.
var DefaultThresholds = Thresholds{Unsure: 0.5, Spam: 0.9}

// Verdict is the classification result.
type Verdict struct {
	// Score is the combined probability of spam, 0..1.
	Score float64 `json:"score"`
	// Bayes is the local classifier's score (0.5 if untrained).
	Bayes   float64       `json:"bayes"`
	Trained bool          `json:"trained"` // enough training data for the local model
	Server  ServerVerdict `json:"server"`
	Level   Level         `json:"level"`
	IsSpam  bool          `json:"isSpam"`
	// Reasons are short English explanations (server score, auth failures,
	// strongest tokens).
	Reasons []string `json:"reasons,omitempty"`
}

// Percent returns the score as 0..100.
func (v Verdict) Percent() int { return int(math.Round(v.Score * 100)) }

// Classifier classifies and trains with a TokenStore.
type Classifier struct {
	Store      TokenStore
	Thresholds Thresholds
	// MinTraining is the number of spam and of ham messages needed before
	// the local model is used (default 5 each).
	MinTraining int
}

const (
	robinsonS = 1.0 // strength of the prior
	robinsonX = 0.5 // prior probability for unknown tokens
	minDev    = 0.1 // tokens closer than this to 0.5 are ignored
	maxClues  = 150
)

// Classify scores a message.
func (c *Classifier) Classify(ctx context.Context, m Message) (Verdict, error) {
	return c.ClassifyTokens(ctx, Tokenize(m), ParseServer(m))
}

// ClassifyTokens scores pre-computed tokens plus a server verdict.
func (c *Classifier) ClassifyTokens(ctx context.Context, tokens []string, sv ServerVerdict) (Verdict, error) {
	v := Verdict{Bayes: 0.5, Server: sv}
	th := c.Thresholds
	if th.Unsure <= 0 {
		th.Unsure = DefaultThresholds.Unsure
	}
	if th.Spam <= 0 {
		th.Spam = DefaultThresholds.Spam
	}
	minT := c.MinTraining
	if minT <= 0 {
		minT = 5
	}
	var logit float64
	if c.Store != nil {
		ns, nh, err := c.Store.SpamTotals(ctx)
		if err != nil {
			return v, err
		}
		if ns >= minT && nh >= minT {
			hashes := make([]int64, len(tokens))
			for i, t := range tokens {
				hashes[i] = Hash(t)
			}
			counts, err := c.Store.SpamCounts(ctx, hashes)
			if err != nil {
				return v, err
			}
			score, clues := fisher(tokens, hashes, counts, ns, nh)
			v.Bayes, v.Trained = score, true
			logit += 1.2 * logitOf(score)
			v.Reasons = append(v.Reasons, clues...)
		}
	}
	sl, why := sv.logit()
	logit += sl
	v.Reasons = append(why, v.Reasons...)
	v.Score = sigmoid(logit)
	switch {
	case v.Score >= th.Spam:
		v.Level = LevelSpam
	case v.Score >= th.Unsure:
		v.Level = LevelUnsure
	default:
		v.Level = LevelHam
	}
	if !v.Trained && !sv.Present && v.Level == LevelUnsure && v.Score < 0.55 {
		// No information at all: do not show "unsure" for every mail.
		v.Level = LevelHam
	}
	v.IsSpam = v.Level == LevelSpam
	return v, nil
}

// Train learns a message as spam or ham. Use Untrain first when the user
// corrects an earlier training.
func (c *Classifier) Train(ctx context.Context, tokens []string, spam bool) error {
	return c.Store.SpamTrain(ctx, hashAll(tokens), spam, 1)
}

// Untrain removes an earlier training.
func (c *Classifier) Untrain(ctx context.Context, tokens []string, spam bool) error {
	return c.Store.SpamTrain(ctx, hashAll(tokens), spam, -1)
}

func hashAll(tokens []string) []int64 {
	out := make([]int64, len(tokens))
	for i, t := range tokens {
		out[i] = Hash(t)
	}
	return out
}

type clue struct {
	token string
	p     float64
}

// fisher implements Robinson's chi-square combining (as in SpamBayes):
// returns the score and the strongest tokens as reasons.
func fisher(tokens []string, hashes []int64, counts map[int64]Count, ns, nh int) (float64, []string) {
	var clues []clue
	for i, h := range hashes {
		cnt := counts[h]
		n := float64(cnt.Spam + cnt.Ham)
		if n == 0 {
			continue
		}
		sr := float64(cnt.Spam) / float64(ns)
		hr := float64(cnt.Ham) / float64(nh)
		if sr > 1 {
			sr = 1
		}
		if hr > 1 {
			hr = 1
		}
		p := sr / (sr + hr)
		f := (robinsonS*robinsonX + n*p) / (robinsonS + n)
		if math.Abs(f-0.5) >= minDev {
			clues = append(clues, clue{tokens[i], f})
		}
	}
	if len(clues) == 0 {
		return 0.5, nil
	}
	sort.Slice(clues, func(i, j int) bool {
		di, dj := math.Abs(clues[i].p-0.5), math.Abs(clues[j].p-0.5)
		if di != dj {
			return di > dj
		}
		return clues[i].token < clues[j].token
	})
	if len(clues) > maxClues {
		clues = clues[:maxClues]
	}
	var sumLnP, sumLn1P float64
	for _, cl := range clues {
		p := math.Min(math.Max(cl.p, 1e-6), 1-1e-6)
		sumLnP += math.Log(p)
		sumLn1P += math.Log(1 - p)
	}
	n := len(clues)
	spamminess := 1 - chi2Q(-2*sumLn1P, 2*n)
	hamminess := 1 - chi2Q(-2*sumLnP, 2*n)
	score := (spamminess - hamminess + 1) / 2
	var why []string
	for i, cl := range clues {
		if i >= 3 {
			break
		}
		if cl.p > 0.5 {
			why = append(why, "spam token "+cl.token)
		} else {
			why = append(why, "ham token "+cl.token)
		}
	}
	return score, why
}

// chi2Q is the survival function of the chi-square distribution with an
// even number of degrees of freedom.
func chi2Q(x2 float64, v int) float64 {
	m := x2 / 2
	sum := math.Exp(-m)
	term := sum
	for i := 1; i < v/2; i++ {
		term *= m / float64(i)
		sum += term
	}
	return math.Min(sum, 1)
}

func logitOf(p float64) float64 {
	p = math.Min(math.Max(p, 1e-4), 1-1e-4)
	return math.Log(p / (1 - p))
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }
