// SPDX-License-Identifier: AGPL-3.0-or-later

// Package thunderbird reads a Thunderbird profile (KONZEPT 13.1): accounts,
// identities and signatures from prefs.js, the local mail folders (mbox
// trees, read with package mbox), address books (abook.sqlite and other
// *.sqlite address books of Thunderbird 102+) and hints about TbSync
// accounts (CalDAV/CardDAV/EAS), which have to be set up again in TuxMail.
//
// Passwords are never read: they are encrypted in logins.json with the
// Thunderbird primary password (NSS) and are entered again in TuxMail.
package thunderbird

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/emersion/go-vcard"

	ie "github.com/Midas1989/Mailclient/internal/importexport"
	"github.com/Midas1989/Mailclient/internal/importexport/mbox"
	"github.com/Midas1989/Mailclient/internal/importexport/vcf"

	_ "modernc.org/sqlite" // address books
)

// Server is an incoming or outgoing server.
type Server struct {
	Kind     string `json:"kind"` // imap | pop3 | smtp | none (Local Folders) | nntp | rss
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	TLS      string `json:"tls,omitempty"` // tls | starttls | none
	Username string `json:"username,omitempty"`
	Auth     string `json:"auth,omitempty"` // password | oauth2
}

// Identity is a sender identity with its signature.
type Identity struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	ReplyTo       string `json:"replyTo,omitempty"`
	Organization  string `json:"organization,omitempty"`
	SignatureHTML string `json:"signatureHtml,omitempty"`
	SignatureText string `json:"signatureText,omitempty"`
}

// Account is one Thunderbird account.
type Account struct {
	Key        string     `json:"key"` // account1 ...
	Name       string     `json:"name"`
	Incoming   Server     `json:"incoming"`
	Outgoing   *Server    `json:"outgoing,omitempty"`
	Identities []Identity `json:"identities"`
	// MailDir is the local mail directory of the account (mbox tree).
	MailDir string `json:"mailDir,omitempty"`
}

// AddressBook is an address book of the profile.
type AddressBook struct {
	Name  string        `json:"name"`
	File  string        `json:"file"`
	Cards []vcard.Card  `json:"-"`
	Count int           `json:"count"`
	Lists []ContactList `json:"lists,omitempty"`
}

// ContactList is a mailing list of an address book.
type ContactList struct {
	Name    string   `json:"name"`
	Members []string `json:"members"` // e-mail addresses
}

// Profile is the content of a Thunderbird profile.
type Profile struct {
	Dir          string        `json:"dir"`
	Accounts     []Account     `json:"accounts"`
	AddressBooks []AddressBook `json:"addressBooks"`
	TbSync       []string      `json:"tbsync,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
}

// LocalFoldersAccount returns the "Local Folders" account (server type none).
func (p *Profile) LocalFoldersAccount() *Account {
	for i := range p.Accounts {
		if p.Accounts[i].Incoming.Kind == "none" {
			return &p.Accounts[i]
		}
	}
	return nil
}

// ==== profile discovery ========================================================

// DefaultRoots returns the usual Thunderbird profile roots of this system.
func DefaultRoots() []string {
	home, _ := os.UserHomeDir()
	var out []string
	switch runtime.GOOS {
	case "windows":
		if a := os.Getenv("APPDATA"); a != "" {
			out = append(out, filepath.Join(a, "Thunderbird"))
		}
	case "darwin":
		out = append(out, filepath.Join(home, "Library", "Thunderbird"))
	default:
		out = append(out, filepath.Join(home, ".thunderbird"), filepath.Join(home, "snap", "thunderbird", "common", ".thunderbird"),
			filepath.Join(home, ".var", "app", "org.mozilla.Thunderbird", ".thunderbird"))
	}
	return out
}

// ResolveProfile accepts a profile directory (with prefs.js) or a
// Thunderbird root (with profiles.ini; the default profile is used).
func ResolveProfile(p string) (string, error) {
	if fileExists(filepath.Join(p, "prefs.js")) {
		return p, nil
	}
	ini := filepath.Join(p, "profiles.ini")
	if !fileExists(ini) {
		return "", ie.Corrupt("%s is neither a Thunderbird profile nor a profile root", p)
	}
	profiles, err := ParseProfilesIni(ini)
	if err != nil {
		return "", err
	}
	for _, pr := range profiles {
		if pr.Default && fileExists(filepath.Join(pr.Path, "prefs.js")) {
			return pr.Path, nil
		}
	}
	for _, pr := range profiles {
		if fileExists(filepath.Join(pr.Path, "prefs.js")) {
			return pr.Path, nil
		}
	}
	return "", ie.Corrupt("no profile with prefs.js in %s", p)
}

// ProfileEntry is a profile listed in profiles.ini.
type ProfileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Default bool   `json:"default"`
}

// ParseProfilesIni reads profiles.ini.
func ParseProfilesIni(path string) ([]ProfileEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	root := filepath.Dir(path)
	var (
		out      []ProfileEntry
		cur      *ProfileEntry
		relative = true
		install  string
	)
	sc := bufio.NewScanner(f)
	finish := func() {
		if cur != nil && cur.Path != "" {
			if relative {
				cur.Path = filepath.Join(root, filepath.FromSlash(cur.Path))
			}
			out = append(out, *cur)
		}
		cur, relative = nil, true
	}
	section := ""
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") {
			finish()
			section = strings.Trim(l, "[]")
			if strings.HasPrefix(section, "Profile") {
				cur = &ProfileEntry{}
			}
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(section, "Install") && k == "Default":
			install = v
		case cur != nil && k == "Name":
			cur.Name = v
		case cur != nil && k == "Path":
			cur.Path = v
		case cur != nil && k == "IsRelative":
			relative = v == "1"
		case cur != nil && k == "Default":
			cur.Default = v == "1"
		}
	}
	finish()
	if install != "" {
		for i := range out {
			out[i].Default = strings.HasSuffix(filepath.ToSlash(out[i].Path), "/"+install) || filepath.ToSlash(out[i].Path) == install
		}
	}
	return out, sc.Err()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ==== prefs.js ================================================================

var prefRe = regexp.MustCompile(`^\s*user_pref\(\s*"((?:[^"\\]|\\.)*)"\s*,\s*(.*)\)\s*;\s*$`)

// Prefs are the parsed user_pref() values (strings, numbers as strings,
// booleans as "true"/"false").
type Prefs map[string]string

// ParsePrefs reads prefs.js.
func ParsePrefs(r io.Reader) (Prefs, error) {
	p := Prefs{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		m := prefRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		key := unescapeJS(m[1])
		v := strings.TrimSpace(m[2])
		if strings.HasPrefix(v, `"`) {
			var s string
			if err := json.Unmarshal([]byte(v), &s); err != nil {
				s = unescapeJS(strings.Trim(v, `"`))
			}
			v = s
		}
		p[key] = v
	}
	return p, sc.Err()
}

func unescapeJS(s string) string {
	var s2 string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &s2); err == nil {
		return s2
	}
	return strings.ReplaceAll(s, `\"`, `"`)
}

func (p Prefs) list(key string) []string {
	var out []string
	for _, v := range strings.Split(p[key], ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (p Prefs) int(key string) int {
	n, _ := strconv.Atoi(p[key])
	return n
}

// path resolves a "*-rel" preference ("[ProfD]Mail/Local Folders") or the
// absolute variant.
func (p Prefs) path(profile, key string) string {
	if rel := p[key+"-rel"]; rel != "" {
		if r, ok := strings.CutPrefix(rel, "[ProfD]"); ok {
			return filepath.Join(profile, filepath.FromSlash(r))
		}
	}
	if abs := p[key]; abs != "" {
		return abs
	}
	return ""
}

func tlsMode(socketType int) string {
	switch socketType {
	case 2:
		return "starttls"
	case 3:
		return "tls"
	default:
		return "none"
	}
}

func authMode(m int) string {
	if m == 10 {
		return "oauth2"
	}
	return "password"
}

// ==== reading ==================================================================

// Options select what Read loads.
type Options struct {
	// SkipAddressBooks does not open the *.sqlite address books.
	SkipAddressBooks bool
	// Root confines every file the profile refers to (the profile itself
	// via profiles.ini, local mail directories, signature files) to this
	// directory. It must be set for untrusted profiles such as uploaded
	// ZIP files in server mode: prefs.js and profiles.ini can name
	// absolute paths ("sig_file" = master.key) or "[ProfD]../..".
	Root string
}

// within reports whether p is root or below it, after resolving symlinks.
func within(root, p string) bool {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	q, err := filepath.EvalSymlinks(p)
	if err != nil {
		// A path that does not exist yet: check it lexically.
		q = filepath.Clean(p)
	}
	rel, err := filepath.Rel(r, q)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// Read reads the profile at dir (see ResolveProfile).
func Read(ctx context.Context, dir string, opts Options) (*Profile, error) {
	dir, err := ResolveProfile(dir)
	if err != nil {
		return nil, err
	}
	if opts.Root != "" && !within(opts.Root, dir) {
		return nil, ie.Corrupt("profile %s is outside the imported directory", filepath.Base(dir))
	}
	allowed := func(p string) bool { return opts.Root == "" || within(opts.Root, p) }
	f, err := os.Open(filepath.Join(dir, "prefs.js"))
	if err != nil {
		return nil, err
	}
	prefs, err := ParsePrefs(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	pr := &Profile{Dir: dir}
	smtp := map[string]*Server{}
	for _, key := range prefs.list("mail.smtpservers") {
		base := "mail.smtpserver." + key + "."
		tls := "none"
		switch prefs.int(base + "try_ssl") {
		case 2:
			tls = "starttls"
		case 3:
			tls = "tls"
		}
		port := prefs.int(base + "port")
		if port == 0 {
			port = map[string]int{"tls": 465, "starttls": 587, "none": 25}[tls]
		}
		smtp[key] = &Server{Kind: "smtp", Host: prefs[base+"hostname"], Port: port, TLS: tls, Username: prefs[base+"username"],
			Auth: authMode(prefs.int(base + "authMethod"))}
	}
	defaultSMTP := prefs["mail.smtp.defaultserver"]
	for _, key := range prefs.list("mail.accountmanager.accounts") {
		base := "mail.account." + key + "."
		srv := prefs[base+"server"]
		if srv == "" {
			continue
		}
		sb := "mail.server." + srv + "."
		acc := Account{Key: key, Name: prefs[sb+"name"]}
		acc.Incoming = Server{Kind: prefs[sb+"type"], Host: prefs[sb+"hostname"], Port: prefs.int(sb + "port"),
			TLS: tlsMode(prefs.int(sb + "socketType")), Username: prefs[sb+"userName"], Auth: authMode(prefs.int(sb + "authMethod"))}
		if acc.Incoming.Port == 0 {
			acc.Incoming.Port = defaultPort(acc.Incoming.Kind, acc.Incoming.TLS)
		}
		acc.MailDir = prefs.path(dir, sb+"directory")
		if acc.MailDir != "" && !allowed(acc.MailDir) {
			pr.Warnings = append(pr.Warnings, fmt.Sprintf("%s: mail directory outside the profile ignored", key))
			acc.MailDir = ""
		}
		for _, idKey := range prefs.list(base + "identities") {
			ib := "mail.identity." + idKey + "."
			id := Identity{Name: prefs[ib+"fullName"], Email: prefs[ib+"useremail"], ReplyTo: prefs[ib+"reply_to"], Organization: prefs[ib+"organization"]}
			sig := prefs[ib+"htmlSigText"]
			if prefs[ib+"attach_signature"] == "true" {
				if p := prefs.path(dir, ib+"sig_file"); p != "" && allowed(p) {
					if b, err := os.ReadFile(p); err == nil {
						sig = string(b)
					}
				}
			}
			if sig != "" {
				if prefs[ib+"htmlSigFormat"] == "true" || strings.Contains(sig, "<") {
					id.SignatureHTML = sig
				} else {
					id.SignatureText = sig
				}
			}
			if acc.Outgoing == nil {
				sk := prefs[ib+"smtpServer"]
				if sk == "" {
					sk = defaultSMTP
				}
				if s := smtp[sk]; s != nil {
					cp := *s
					acc.Outgoing = &cp
				}
			}
			acc.Identities = append(acc.Identities, id)
		}
		if acc.Name == "" && len(acc.Identities) > 0 {
			acc.Name = acc.Identities[0].Email
		}
		pr.Accounts = append(pr.Accounts, acc)
	}
	if !opts.SkipAddressBooks {
		pr.AddressBooks, pr.Warnings = readAddressBooks(ctx, dir, prefs)
	}
	pr.TbSync = tbsyncHints(dir, prefs)
	if fileExists(filepath.Join(dir, "abook.mab")) && !fileExists(filepath.Join(dir, "abook.sqlite")) {
		pr.Warnings = append(pr.Warnings, "abook.mab (Thunderbird < 78) is not supported; export the address book as vCard or LDIF")
	}
	if fileExists(filepath.Join(dir, "logins.json")) {
		pr.Warnings = append(pr.Warnings, "passwords are not imported; enter them again for each account")
	}
	return pr, nil
}

func defaultPort(kind, tls string) int {
	switch kind {
	case "imap":
		if tls == "tls" {
			return 993
		}
		return 143
	case "pop3":
		if tls == "tls" {
			return 995
		}
		return 110
	}
	return 0
}

// MailSource returns a source for the local mail folders of an account.
func MailSource(acc Account) (ie.Source, error) {
	if acc.MailDir == "" {
		return nil, fmt.Errorf("thunderbird: account %q has no local mail directory", acc.Name)
	}
	return mbox.OpenTree(acc.MailDir, mbox.MboxO), nil
}

// ==== address books ============================================================

func readAddressBooks(ctx context.Context, dir string, prefs Prefs) ([]AddressBook, []string) {
	names := map[string]string{"abook.sqlite": "Personal Address Book", "history.sqlite": "Collected Addresses"}
	for k, v := range prefs {
		if strings.HasPrefix(k, "ldap_2.servers.") && strings.HasSuffix(k, ".filename") {
			base := strings.TrimSuffix(k, "filename")
			if d := prefs[base+"description"]; d != "" {
				names[v] = d
			}
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.sqlite"))
	sort.Strings(matches)
	var (
		out  []AddressBook
		warn []string
	)
	for _, p := range matches {
		base := filepath.Base(p)
		if _, ok := names[base]; !ok && !strings.HasPrefix(base, "abook") && !strings.HasPrefix(base, "impab") {
			continue
		}
		ab, err := ReadAddressBook(ctx, p)
		if err != nil {
			warn = append(warn, fmt.Sprintf("%s: %v", base, err))
			continue
		}
		ab.Name = names[base]
		if ab.Name == "" {
			ab.Name = strings.TrimSuffix(base, ".sqlite")
		}
		out = append(out, ab)
	}
	return out, warn
}

// ReadAddressBook reads a Thunderbird 102+ address book (abook.sqlite).
// The database is copied first, so a running Thunderbird does not block it.
func ReadAddressBook(ctx context.Context, path string) (AddressBook, error) {
	tmp, err := os.CreateTemp("", "tuxmail-abook-*.sqlite")
	if err != nil {
		return AddressBook{}, err
	}
	defer os.Remove(tmp.Name())
	src, err := os.Open(path)
	if err != nil {
		tmp.Close()
		return AddressBook{}, err
	}
	_, err = io.Copy(tmp, src)
	src.Close()
	tmp.Close()
	if err != nil {
		return AddressBook{}, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(tmp.Name())+"?mode=ro&"+url.Values{"_pragma": {"query_only(1)"}}.Encode())
	if err != nil {
		return AddressBook{}, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT card, name, value FROM properties ORDER BY card`)
	if err != nil {
		return AddressBook{}, ie.Corrupt("%s: %v", filepath.Base(path), err)
	}
	props := map[string]map[string]string{}
	var order []string
	for rows.Next() {
		var card, name string
		var value sql.NullString
		if err := rows.Scan(&card, &name, &value); err != nil {
			rows.Close()
			return AddressBook{}, err
		}
		if props[card] == nil {
			props[card] = map[string]string{}
			order = append(order, card)
		}
		props[card][name] = value.String
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return AddressBook{}, err
	}
	ab := AddressBook{File: path}
	byUID := map[string]string{}
	for _, uid := range order {
		p := props[uid]
		var card vcard.Card
		if raw := p["_vCard"]; raw != "" {
			if cards, err := vcf.Parse(strings.NewReader(raw)); err == nil && len(cards) > 0 {
				card = cards[0]
			}
		}
		if card == nil {
			card = cardFromProps(p)
		}
		if card.Value(vcard.FieldUID) == "" {
			card.SetValue(vcard.FieldUID, uid)
		}
		byUID[uid] = card.PreferredValue(vcard.FieldEmail)
		ab.Cards = append(ab.Cards, card)
	}
	ab.Count = len(ab.Cards)
	// Mailing lists (optional tables).
	if lrows, err := db.QueryContext(ctx, `SELECT l.uid, l.name, lc.card FROM lists l LEFT JOIN list_cards lc ON lc.list = l.uid ORDER BY l.name`); err == nil {
		lists := map[string]*ContactList{}
		var lorder []string
		for lrows.Next() {
			var luid, name string
			var card sql.NullString
			if lrows.Scan(&luid, &name, &card) != nil {
				break
			}
			cl := lists[luid]
			if cl == nil {
				cl = &ContactList{Name: name}
				lists[luid] = cl
				lorder = append(lorder, luid)
			}
			if e := byUID[card.String]; e != "" {
				cl.Members = append(cl.Members, e)
			}
		}
		lrows.Close()
		for _, k := range lorder {
			ab.Lists = append(ab.Lists, *lists[k])
		}
	}
	return ab, nil
}

func cardFromProps(p map[string]string) vcard.Card {
	c := vcard.Card{}
	c.SetValue(vcard.FieldVersion, "4.0")
	fn := p["DisplayName"]
	if fn == "" {
		fn = strings.TrimSpace(p["FirstName"] + " " + p["LastName"])
	}
	if fn == "" {
		fn = p["PrimaryEmail"]
	}
	c.SetValue(vcard.FieldFormattedName, fn)
	if p["FirstName"] != "" || p["LastName"] != "" {
		c.SetName(&vcard.Name{GivenName: p["FirstName"], FamilyName: p["LastName"]})
	}
	if v := p["NickName"]; v != "" {
		c.SetValue(vcard.FieldNickname, v)
	}
	for i, k := range []string{"PrimaryEmail", "SecondEmail"} {
		if v := p[k]; v != "" {
			f := &vcard.Field{Value: v, Params: vcard.Params{}}
			if i == 0 {
				f.Params.Set(vcard.ParamPreferred, "1")
			}
			c.Add(vcard.FieldEmail, f)
		}
	}
	for k, typ := range map[string]string{"WorkPhone": vcard.TypeWork, "HomePhone": vcard.TypeHome, "CellularNumber": vcard.TypeCell,
		"FaxNumber": vcard.TypeFax, "PagerNumber": vcard.TypePager} {
		if v := p[k]; v != "" {
			c.Add(vcard.FieldTelephone, &vcard.Field{Value: v, Params: vcard.Params{vcard.ParamType: {typ}}})
		}
	}
	if v := p["Company"]; v != "" {
		c.SetValue(vcard.FieldOrganization, v)
	}
	if v := p["JobTitle"]; v != "" {
		c.SetValue(vcard.FieldTitle, v)
	}
	if v := p["Notes"]; v != "" {
		c.SetValue(vcard.FieldNote, v)
	}
	if v := p["WebPage1"]; v != "" {
		c.SetValue(vcard.FieldURL, v)
	}
	if p["HomeAddress"] != "" || p["HomeCity"] != "" {
		c.AddAddress(&vcard.Address{StreetAddress: strings.TrimSpace(p["HomeAddress"] + " " + p["HomeAddress2"]), Locality: p["HomeCity"],
			Region: p["HomeState"], PostalCode: p["HomeZipCode"], Country: p["HomeCountry"], Field: &vcard.Field{Params: vcard.Params{vcard.ParamType: {vcard.TypeHome}}}})
	}
	if p["WorkAddress"] != "" || p["WorkCity"] != "" {
		c.AddAddress(&vcard.Address{StreetAddress: strings.TrimSpace(p["WorkAddress"] + " " + p["WorkAddress2"]), Locality: p["WorkCity"],
			Region: p["WorkState"], PostalCode: p["WorkZipCode"], Country: p["WorkCountry"], Field: &vcard.Field{Params: vcard.Params{vcard.ParamType: {vcard.TypeWork}}}})
	}
	return c
}

// ==== TbSync ===================================================================

// tbsyncHints lists TbSync accounts (CalDAV/CardDAV/Exchange ActiveSync)
// found in the profile so that the user can set them up again.
func tbsyncHints(dir string, prefs Prefs) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for k := range prefs {
		if strings.HasPrefix(k, "extensions.tbsync.") || strings.HasPrefix(k, "extensions.dav4tbsync.") || strings.HasPrefix(k, "extensions.eas4tbsync.") {
			add("TbSync is installed")
			break
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "TbSync", "*.json"))
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		collectTbSync(doc, add)
	}
	return out
}

func collectTbSync(v any, add func(string)) {
	switch x := v.(type) {
	case map[string]any:
		name, _ := x["accountname"].(string)
		provider, _ := x["provider"].(string)
		host, _ := x["host"].(string)
		if host == "" {
			host, _ = x["serviceprovider"].(string)
		}
		user, _ := x["user"].(string)
		if name != "" && provider != "" {
			add(strings.TrimSpace(fmt.Sprintf("%s (%s) %s %s", name, provider, host, user)))
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			collectTbSync(x[k], add)
		}
	case []any:
		for _, e := range x {
			collectTbSync(e, add)
		}
	}
}

// ErrNoProfile is returned by FindDefaultProfile when none exists.
var ErrNoProfile = errors.New("thunderbird: no profile found")

// FindDefaultProfile looks for the default profile in DefaultRoots.
func FindDefaultProfile() (string, error) {
	for _, r := range DefaultRoots() {
		if p, err := ResolveProfile(r); err == nil {
			return p, nil
		}
	}
	return "", ErrNoProfile
}
