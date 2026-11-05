// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"errors"
	"strings"
	"testing"
)

// NC-02: every builtin that returns a string, slice or map is charged against
// the per-evaluation budget; replace()/split() size their result before
// allocating it.
func TestSandboxBudgetCoversBuiltinFamilies(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	many := make([]string, 1_100_000) // 17.6 MB of slice headers > MaxExprStringBytes
	env := &Env{Mail: MailEnv{Body: big, To: many}}
	for name, src := range map[string]string{
		// audit PoCs: 64 MiB result / 1 000 000 list elements
		"replace-amplify":  `len(replace(repeat("a", 8192), "a", repeat("b", 8192))) > 0`,
		"replace-n":        `len(replace(repeat("a", 8192), "a", repeat("b", 8192), 9000)) > 0`,
		"split-empty":      `len(split(repeat("a", 1000000), "")) > 0`,
		"splitAfter-empty": `len(splitAfter(repeat("a", 1000000), "")) > 0`,
		"split-n":          `len(split(repeat("a,", 100000), ",", 100000)) > 0`,
		"splitAfter-n":     `len(splitAfter(repeat("a,", 100000), ",", 100000)) > 0`,
		"upper":            `all(1..40, {len(upper(mail.body)) > 0})`,
		"lower":            `all(1..40, {len(lower(mail.body)) > 0})`,
		"trim":             `all(1..40, {len(trim(mail.body)) > 0})`,
		"trim-cutset":      `all(1..40, {len(trim(mail.body, "y")) > 0})`,
		"trimPrefix":       `all(1..40, {len(trimPrefix(mail.body, "y")) > 0})`,
		"trimSuffix":       `all(1..40, {len(trimSuffix(mail.body, "y")) > 0})`,
		"take":             `len(take(mail.to, 1100000)) > 0`,
	} {
		p, err := compileExpr(src)
		if err != nil {
			t.Errorf("%s: compile: %v", name, err)
			continue
		}
		_, err = p.Eval(env)
		if err == nil {
			t.Errorf("%s: %s evaluated without hitting a limit", name, src)
		} else if !errors.Is(err, ErrExprBudget) && !strings.Contains(err.Error(), "memory budget") {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
}

// Every builtin that can return a string/slice/map is routed through a guard.
func TestSandboxGuardsAreInstalled(t *testing.T) {
	for _, src := range []string{
		`len(map(mail.to, {#})) > 0`,
		`len(filter(mail.to, {# != ""})) > 0`,
		`len(sortBy(mail.to, {#})) > 0`,
		`len(groupBy(mail.to, {#})) > 0`,
		`len(concat(mail.to, mail.to)) > 0`,
		`len(flatten([mail.to])) > 0`,
		`len(uniq(mail.to)) > 0`,
		`len(sort(mail.to)) > 0`,
		`len(reverse(mail.to)) > 0`,
		`len(take(mail.to, 1)) > 0`,
		`len(keys({"a": 1})) > 0`,
		`len(values({"a": 1})) > 0`,
		`len(toPairs({"a": 1})) > 0`,
		`len(fromPairs([["a", 1]])) > 0`,
		`upper(mail.subject) != ""`,
		`lower(mail.subject) != ""`,
		`trim(mail.subject) != ""`,
		`trimPrefix(mail.subject, "a") != ""`,
		`trimSuffix(mail.subject, "a") != ""`,
		`len(split(mail.subject, " ")) > 0`,
		`len(splitAfter(mail.subject, " ")) > 0`,
		`replace(mail.subject, "a", "b") != ""`,
		`repeat("a", 2) != ""`,
		`join(mail.to) != ""`,
	} {
		p, err := compileExpr(src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		dis := p.p.Disassemble()
		if !strings.Contains(dis, "__guard") && !strings.Contains(dis, "__replace") &&
			!strings.Contains(dis, "__split") && !strings.Contains(dis, "__repeat") && !strings.Contains(dis, "__join") {
			t.Errorf("%s: no budget function in the program:\n%s", src, dis)
		}
	}
}

func TestSandboxBuiltinSemantics(t *testing.T) {
	env := &Env{Mail: MailEnv{Subject: "  Hello World  ", To: []string{"b@y.de", "a@x.de"}}}
	for _, src := range []string{
		`replace("aaa", "a", "b") == "bbb"`,
		`replace("aaa", "a", "b", 2) == "bba"`,
		`replace("aaa", "a", "", -1) == ""`,
		`replace("abc", "", "-") == "-a-b-c-"`,
		`replace("abc", "x", "y") == "abc"`,
		`split("a,b,c", ",") == ["a", "b", "c"]`,
		`split("a,b,c", ",", 2) == ["a", "b,c"]`,
		`len(split("a,b,c", ",", 0)) == 0`,
		`splitAfter("a,b", ",") == ["a,", "b"]`,
		`splitAfter("a,b,c", ",", 2) == ["a,", "b,c"]`,
		`len(split("abc", "")) == 3`,
		`len(split(mail.subject, " ")) == 6`,
		`trim(mail.subject) == "Hello World"`,
		`trim("xxhixx", "x") == "hi"`,
		`trimPrefix("prefix-a", "prefix-") == "a"`,
		`trimSuffix("a.txt", ".txt") == "a"`,
		`upper("abc") == "ABC" && lower("ABC") == "abc"`,
		`concat([1], [2]) == [1, 2]`,
		`flatten([[1], [2]]) == [1, 2]`,
		`sort(mail.to) == ["a@x.de", "b@y.de"]`,
		`sortBy(mail.to, {#}) == ["a@x.de", "b@y.de"]`,
		`len(map(mail.to, {upper(#)})) == 2 && map(mail.to, {upper(#)})[0] == "B@Y.DE"`,
		`first(mail.to) == "b@y.de" && last(mail.to) == "a@x.de"`,
		`len(keys({"a": 1})) == 1 && len(values({"a": 1})) == 1`,
		`len(uniq(["a", "a"])) == 1 && reverse([1, 2])[0] == 2`,
		`upper(lower("AbC")) + "!" == "ABC!"`,
		`reduce(1..3, #acc + upper("a"), "") == "AAA"`,
	} {
		p, err := compileExpr(src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		ok, err := p.Eval(env)
		if err != nil || !ok {
			t.Errorf("%s = %v, %v", src, ok, err)
		}
	}
}
