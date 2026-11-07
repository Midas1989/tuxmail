// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Midas1989/Mailclient/internal/domain"
)

// What the AI assistant may see (KONZEPT 8.10): every source - each mail and
// messenger account, calendar, address book, cloud account and the projects -
// has one of three levels. A source without an entry is "off": new accounts
// are never shared without a decision of the user (metadata such as sender
// and subject are personal data, too). Every function that hands data to an
// AI provider asks the aiScope of this file; the settings page only shows it.

// AI access levels.
const (
	AiLevelOff      = "off"      // nothing of the source is sent
	AiLevelMetadata = "metadata" // sender, subject, time; no text
	AiLevelContent  = "content"  // text, too
)

// aiAccessVersion 2: Sources is authoritative (before: AccountPermissions,
// absent = allowed). Older settings are migrated once (migrateAiAccess).
const aiAccessVersion = 2

// CodeAINoAccess: the user has not shared the data of this action with the AI.
const CodeAINoAccess = "ai_no_access"

var errAINoAccess = &Error{Code: CodeAINoAccess, Status: http.StatusForbidden,
	Message: "the AI assistant has no access to this data (Settings > AI assistant)"}

// AI source kinds and keys.
const (
	aiKindMail        = "mail"
	aiKindMessenger   = "messenger"
	aiKindCalendar    = "calendar"
	aiKindAddressBook = "addressbook"
	aiKindCloud       = "cloud"
	aiKindProjects    = "projects"
	aiKeyProjects     = "projects"
)

func aiKey(kind string, id int64) string { return kind + ":" + strconv.FormatInt(id, 10) }

// aiAccountKey is the key of a mail or messenger account.
func aiAccountKey(id int64) string { return aiKey("account", id) }

// aiAccountKind tells whether an account is a mail or messenger source ("" = neither).
func aiAccountKind(k domain.AccountKind) string {
	switch k {
	case domain.KindIMAP, domain.KindPOP3, domain.KindJMAP, domain.KindEAS, domain.KindGraph, domain.KindEWS, domain.KindGmail, kindLocal:
		return aiKindMail
	}
	if isMessengerKind(k) {
		return aiKindMessenger
	}
	return ""
}

func aiRank(l string) int {
	switch l {
	case AiLevelContent:
		return 2
	case AiLevelMetadata:
		return 1
	}
	return 0
}

// aiMin returns the lower level; "" (no cap) does not limit.
func aiMin(l, limit string) string {
	if aiRank(l) == 0 {
		return AiLevelOff
	}
	if limit != "" && aiRank(limit) < aiRank(l) {
		return aiMin(limit, "")
	}
	return l
}

func validAiLevel(l string) bool {
	return l == AiLevelOff || l == AiLevelMetadata || l == AiLevelContent
}

// migrateAiAccess converts settings of version 1 (found = they were stored
// before) to Sources: every mail/messenger account keeps what it had -
// content unless it was switched off - and everything that was never sent
// (calendars, contacts, files, projects) stays off. Fresh installations
// start with everything off. It reports whether s changed.
func migrateAiAccess(s *AiSettings, found bool, accounts []domain.Account) bool {
	if s.AccessVersion >= aiAccessVersion {
		if s.Sources == nil {
			s.Sources = map[string]AiSourceAccess{}
		}
		return false
	}
	if s.Sources == nil {
		s.Sources = map[string]AiSourceAccess{}
	}
	if found {
		for _, acc := range accounts {
			if aiAccountKind(acc.Kind) == "" {
				continue
			}
			key := aiAccountKey(acc.ID)
			if _, ok := s.Sources[key]; ok {
				continue
			}
			level := AiLevelContent
			if allowed, ok := s.AccountPermissions[strconv.FormatInt(acc.ID, 10)]; ok && !allowed {
				level = AiLevelOff
			}
			s.Sources[key] = AiSourceAccess{Level: level}
		}
	}
	s.AccessVersion = aiAccessVersion
	return found
}

// legacyAccountPermissions writes the version 1 field for an older TuxMail
// opening the same data (downgrade): an account is only "allowed" there with
// content access here, so the old version never sends more.
func legacyAccountPermissions(s AiSettings, accounts []domain.Account) map[string]bool {
	out := map[string]bool{}
	for _, acc := range accounts {
		if aiAccountKind(acc.Kind) == "" {
			continue
		}
		out[strconv.FormatInt(acc.ID, 10)] = aiMin(s.Sources[aiAccountKey(acc.ID)].Level, "") == AiLevelContent
	}
	return out
}

// aiScope answers what the AI may see for one request.
type aiScope struct {
	a     *App
	s     AiSettings
	max   string                      // admin cap ("" = none)
	roles map[int64]domain.FolderRole // folder id -> role (cache)
}

func (a *App) newAiScope(s AiSettings) *aiScope {
	return &aiScope{a: a, s: s, max: a.opts.AIMaxLevel, roles: map[int64]domain.FolderRole{}}
}

// level returns the effective level of a source.
func (sc *aiScope) level(key string) string {
	return aiMin(aiMin(sc.s.Sources[key].Level, ""), sc.max)
}

// accountLevel returns the level of a mail/messenger item of the account in
// the given folders: excluded folders (junk and trash unless allowed, and
// those chosen by the user) are off.
func (sc *aiScope) accountLevel(ctx context.Context, accountID int64, folderIDs []int64) string {
	acc := sc.s.Sources[aiAccountKey(accountID)]
	l := sc.level(aiAccountKey(accountID))
	if l == AiLevelOff {
		return l
	}
	for _, f := range folderIDs {
		for _, x := range acc.ExcludeFolders {
			if x == f {
				return AiLevelOff
			}
		}
		if acc.AllowJunkTrash {
			continue
		}
		role, ok := sc.roles[f]
		if !ok {
			if fo, err := sc.a.st.GetFolder(ctx, f); err == nil {
				role = fo.Role
			} else {
				role = domain.RoleJunk // unknown folder: do not risk it
			}
			sc.roles[f] = role
		}
		if role == domain.RoleJunk || role == domain.RoleTrash {
			return AiLevelOff
		}
	}
	return l
}

// itemLevel returns the level of a conversation item; encrypted and
// deleted items are never sent.
func (sc *aiScope) itemLevel(ctx context.Context, it ConversationItem) string {
	if it.Encrypted || it.Deleted {
		return AiLevelOff
	}
	return sc.accountLevel(ctx, it.AccountID, it.FolderIDs)
}

// aiCatalog lists every current source with its effective level.
func (a *App) aiCatalog(ctx context.Context, sc *aiScope) []AiSource {
	out := []AiSource{}
	accs, _ := a.st.ListAccounts(ctx)
	for _, acc := range accs {
		kind := aiAccountKind(acc.Kind)
		if kind == "" {
			continue
		}
		key := aiAccountKey(acc.ID)
		src := AiSource{Key: key, Kind: kind, Name: acc.DisplayName, Level: sc.level(key)}
		if kind == aiKindMail {
			src.Folders = []AiFolder{}
			if fs, err := a.st.ListFolders(ctx, acc.ID); err == nil {
				for _, f := range fs {
					src.Folders = append(src.Folders, AiFolder{ID: f.ID, Name: f.Name, Role: string(f.Role)})
				}
			}
		}
		out = append(out, src)
	}
	if cals, err := a.ListCalendars(ctx); err == nil {
		for _, c := range cals {
			key := aiKey(aiKindCalendar, c.ID)
			out = append(out, AiSource{Key: key, Kind: aiKindCalendar, Name: c.Name, Detail: c.AccountName, Level: sc.level(key)})
		}
	}
	if books, err := a.ListAddressBooks(ctx); err == nil {
		for _, b := range books {
			key := aiKey(aiKindAddressBook, b.ID)
			out = append(out, AiSource{Key: key, Kind: aiKindAddressBook, Name: b.Name, Detail: b.AccountName, Level: sc.level(key)})
		}
	}
	if clouds, err := a.ListCloudAccounts(ctx); err == nil {
		for _, c := range clouds {
			if c.Provider == string(domain.KindVaultwarden) {
				continue // a password vault is never an AI source
			}
			key := aiKey(aiKindCloud, c.ID)
			out = append(out, AiSource{Key: key, Kind: aiKindCloud, Name: c.DisplayName, Detail: c.Server, Level: sc.level(key)})
		}
	}
	out = append(out, AiSource{Key: aiKeyProjects, Kind: aiKindProjects, Name: "projects", Level: sc.level(aiKeyProjects)})
	return out
}
