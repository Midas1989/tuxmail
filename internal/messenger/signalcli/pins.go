// SPDX-License-Identifier: AGPL-3.0-or-later

// Package signalcli locates, bundles and installs signal-cli
// (https://github.com/AsamK/signal-cli, GPL-3.0), the external program the
// Signal plugin drives in JSON-RPC mode, together with the Java runtime it
// needs (Eclipse Temurin JRE, GPL-2.0 with Classpath Exception) where no
// native build exists.
//
// Layout of a signal-cli directory (bundled as <plugins>/signal-cli, or
// installed on demand as <data dir>/plugins/signal-cli):
//
//	VERSION                  signal-cli version
//	signal-cli               GraalVM native build (Linux x86_64), or
//	bin/ lib/*.jar           JVM build (started as jre/bin/java -cp lib/* ...)
//	jre/                     Temurin JRE (JVM targets)
//	LICENSE-signal-cli.txt   GPL-3.0
//	NOTICE.txt               sources and licenses
//
// Versions, URLs and checksums are pinned in pins.env (embedded).
package signalcli

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"strings"
)

//go:embed pins.env
var pinsEnv string

// Pins are the pinned downloads (pins.env).
type Pins map[string]string

// LoadPins parses the embedded pins.env.
func LoadPins() Pins { p, _ := ParsePins(pinsEnv); return p }

// ParsePins parses KEY=VALUE lines; # starts a comment line.
func ParsePins(s string) (Pins, error) {
	p := Pins{}
	sc := bufio.NewScanner(strings.NewReader(s))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf("pins line %d: want KEY=VALUE", n)
		}
		p[k] = v
	}
	return p, sc.Err()
}

// Kind of a download.
const (
	KindNative = "native" // GraalVM native signal-cli
	KindJVM    = "jvm"    // signal-cli JVM distribution (bin/, lib/)
	KindJRE    = "jre"    // Java runtime
)

// Download is one pinned file.
type Download struct {
	Kind    string
	Name    string // file name (cache key)
	URL     string
	SHA256  string // lowercase hex; empty = not pinned
	Version string
}

// Plan is what a platform needs.
type Plan struct {
	OS, Arch  string
	Version   string // signal-cli version
	Downloads []Download
	// Note explains platform specifics (e.g. x64 emulation on Windows ARM64).
	Note string
}

// ErrUnsupported: no pinned signal-cli for the platform.
var ErrUnsupported = errors.New("signal-cli: no bundled build for this platform")

// ErrNotPinned: pins.env has no checksum for a download.
var ErrNotPinned = errors.New("signal-cli: download not pinned (empty SHA-256 in pins.env)")

// PlanFor returns the downloads for goos/goarch.
func (p Pins) PlanFor(goos, goarch string) (Plan, error) {
	pl := Plan{OS: goos, Arch: goarch, Version: p["SIGNAL_CLI_VERSION"]}
	jvm := p.download(KindJVM, "SIGNAL_CLI_JVM", pl.Version)
	switch goos + "/" + goarch {
	case "linux/amd64":
		pl.Downloads = []Download{p.download(KindNative, "SIGNAL_CLI_NATIVE", pl.Version)}
	case "linux/arm64":
		pl.Downloads = []Download{jvm, p.download(KindJRE, "TEMURIN_LINUX_AARCH64", p["TEMURIN_VERSION"])}
	case "windows/amd64":
		pl.Downloads = []Download{jvm, p.download(KindJRE, "TEMURIN_WINDOWS_X64", p["TEMURIN_VERSION"])}
	case "windows/arm64":
		pl.Downloads = []Download{jvm, p.download(KindJRE, "TEMURIN_WINDOWS_X64", p["TEMURIN_VERSION"])}
		pl.Note = "x64 Java runtime under Windows 11 x64 emulation (libsignal-client has no Windows ARM64 library)"
	default:
		return pl, fmt.Errorf("%w (%s/%s)", ErrUnsupported, goos, goarch)
	}
	for _, d := range pl.Downloads {
		if d.URL == "" {
			return pl, fmt.Errorf("%w: %s has no URL", ErrUnsupported, d.Kind)
		}
	}
	return pl, nil
}

func (p Pins) download(kind, key, version string) Download {
	u := p[key+"_URL"]
	name := u
	if i := strings.LastIndex(u, "/"); i >= 0 {
		name = u[i+1:]
	}
	return Download{Kind: kind, Name: name, URL: u, SHA256: strings.ToLower(p[key+"_SHA256"]), Version: version}
}

// Pinned reports whether every download of the plan has a checksum.
func (pl Plan) Pinned() error {
	for _, d := range pl.Downloads {
		if d.SHA256 == "" {
			return fmt.Errorf("%w: %s", ErrNotPinned, d.Name)
		}
		if len(d.SHA256) != 64 || strings.Trim(d.SHA256, "0123456789abcdef") != "" {
			return fmt.Errorf("signal-cli: bad SHA-256 for %s in pins.env", d.Name)
		}
	}
	return nil
}
