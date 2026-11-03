// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that is written in YAML as a compact string.
// In addition to time.ParseDuration syntax it accepts the units "d" (days)
// and "w" (weeks), e.g. "3d", "1w2d", "1d12h".
type Duration time.Duration

// D returns the value as time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// ParseDuration parses a duration with optional d/w units.
func ParseDuration(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	var total time.Duration
	rest := s
	for {
		i := strings.IndexAny(rest, "dw")
		if i < 0 {
			break
		}
		num := rest[:i]
		n, err := strconv.Atoi(num)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		unit := 24 * time.Hour
		if rest[i] == 'w' {
			unit *= 7
		}
		total += time.Duration(n) * unit
		rest = rest[i+1:]
	}
	if rest != "" {
		d, err := time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		total += d
	}
	if total < 0 {
		return 0, fmt.Errorf("negative duration %q", s)
	}
	return Duration(total), nil
}

// String formats the duration using d for whole days.
func (d Duration) String() string {
	v := time.Duration(d)
	if v == 0 {
		return "0s"
	}
	var b strings.Builder
	if days := v / (24 * time.Hour); days > 0 {
		fmt.Fprintf(&b, "%dd", days)
		v -= days * 24 * time.Hour
	}
	if v > 0 {
		s := v.String()
		// time.Duration.String renders "1h0m0s"; trim zero tails for readability.
		if strings.HasSuffix(s, "m0s") {
			s = strings.TrimSuffix(s, "0s")
		}
		if strings.HasSuffix(s, "h0m") {
			s = strings.TrimSuffix(s, "0m")
		}
		b.WriteString(s)
	}
	return b.String()
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a string like \"10m\" or \"3d\"", n.Line)
	}
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = v
	return nil
}
