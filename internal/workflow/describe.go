// SPDX-License-Identifier: AGPL-3.0-or-later

package workflow

import (
	"sort"
	"strings"

	"github.com/Midas1989/Mailclient/internal/events"
	"github.com/Midas1989/Mailclient/internal/search/query"
	"github.com/Midas1989/Mailclient/internal/store"
)

// Machine-readable description of the workflow language for editors (expert
// mode, autocomplete, documentation). Everything is JSON-serializable.

// DescriptionVersion is incremented when the structure of Description changes.
const DescriptionVersion = 1

// ParamInfo describes an action parameter.
type ParamInfo struct {
	Name string `json:"name"`
	// Type is one of string, bool, int, duration (e.g. 24h, 3d), size (10MB),
	// list (of strings), map (string to string), enum, query (search
	// language), expr (condition expression).
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"` // allowed values (enum, and list items when set)
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Template    bool     `json:"template,omitempty"` // the value may contain {{placeholders}}
	Example     string   `json:"example,omitempty"`
	// Positional is the position of the parameter in the compact rule
	// syntax (1-based; 0 = only as name=value).
	Positional int `json:"positional,omitempty"`
	// Variadic parameters (lists) take all remaining positional values.
	Variadic bool `json:"variadic,omitempty"`
}

// TriggerInfo describes a trigger event.
type TriggerInfo struct {
	Event       string   `json:"event"`
	Description string   `json:"description"`
	Fields      []string `json:"fields"`  // condition field prefixes filled for this event, e.g. "mail."
	Filters     []string `json:"filters"` // allowed trigger keys besides event
}

// FieldDesc describes a condition field.
type FieldDesc struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // string, list, number, bool, time, map
	Description string   `json:"description,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	Operators   []string `json:"operators,omitempty"` // rule operators (op:) for this type
}

// OperatorInfo describes an operator of rules, expressions or the search language.
type OperatorInfo struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"` // rule, expr, query
	Symbols     []string `json:"symbols,omitempty"`
	Types       []string `json:"types,omitempty"` // field types the rule operator applies to
	Description string   `json:"description"`
}

// FunctionInfo describes an expression function.
type FunctionInfo struct {
	Name        string `json:"name"`
	Signature   string `json:"signature"`
	Description string `json:"description"`
	Example     string `json:"example,omitempty"`
}

// ActionInfo describes an action type.
type ActionInfo struct {
	Type           string      `json:"type"`
	Description    string      `json:"description"`
	ExternalEffect bool        `json:"externalEffect,omitempty"`
	Triggers       []string    `json:"triggers,omitempty"` // empty = all
	Params         []ParamInfo `json:"params"`
}

// OptionInfo describes a workflow option or a common action key.
type OptionInfo struct {
	Path        string   `json:"path"`
	Type        string   `json:"type"`
	Default     string   `json:"default,omitempty"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
}

// PlaceholderInfo describes a template placeholder or helper.
type PlaceholderInfo struct {
	Name        string `json:"name"`
	Alias       string `json:"alias,omitempty"`
	Description string `json:"description"`
	Example     string `json:"example,omitempty"`
}

// QueryFieldInfo describes a field of the search language.
type QueryFieldInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Values      []string `json:"values,omitempty"`
	Ranges      bool     `json:"ranges,omitempty"`
	InMemory    bool     `json:"inMemory"` // evaluated without the database (see MatchEnv)
}

// Description is the complete language description.
type Description struct {
	Version      int               `json:"version"`
	Triggers     []TriggerInfo     `json:"triggers"`
	TriggerKeys  []OptionInfo      `json:"triggerKeys"`
	Fields       []FieldDesc       `json:"fields"`
	DynamicField []FieldDesc       `json:"dynamicFields"` // prefixes like mail.headers.<name>
	Operators    []OperatorInfo    `json:"operators"`
	Functions    []FunctionInfo    `json:"functions"`
	Actions      []ActionInfo      `json:"actions"`
	ActionKeys   []OptionInfo      `json:"actionKeys"` // keys every action accepts (if, continue_on_error)
	Options      []OptionInfo      `json:"options"`
	Placeholders []PlaceholderInfo `json:"placeholders"`
	QueryFields  []QueryFieldInfo  `json:"queryFields"`
	RuleKeywords []string          `json:"ruleKeywords"` // compact rule syntax
	ScopeKeys    []string          `json:"scopeKeys"`    // rule syntax IN key:value
}

var triggerDocs = map[events.Type]TriggerInfo{
	events.MailReceived:          {Description: "A mail arrived", Fields: []string{"mail."}, Filters: []string{"accounts", "account_names", "folders", "search_folders"}},
	events.MailSent:              {Description: "A mail was sent", Fields: []string{"mail."}, Filters: []string{"accounts", "account_names", "folders", "search_folders"}},
	events.MailMoved:             {Description: "A mail was moved (mail.oldFolder is the source)", Fields: []string{"mail."}, Filters: []string{"accounts", "account_names", "folders", "search_folders"}},
	events.MailFlagged:           {Description: "Flags of a mail changed", Fields: []string{"mail."}, Filters: []string{"accounts", "account_names", "folders", "search_folders"}},
	events.MailBeforeSend:        {Description: "Before a mail is sent (synchronous; warn/block)", Fields: []string{"mail."}, Filters: []string{"accounts"}},
	events.ChatReceived:          {Description: "A messenger message arrived", Fields: []string{"chat."}, Filters: []string{"accounts", "channels", "search_folders"}},
	events.CalendarEventStarting: {Description: "A calendar event starts soon", Fields: []string{"calendar."}, Filters: []string{"search_folders"}},
	events.CalendarInviteAnswer:  {Description: "An invitation was answered", Fields: []string{"calendar."}, Filters: []string{"search_folders"}},
	events.ContactCreated:        {Description: "A contact was created", Fields: []string{"contact."}, Filters: []string{"search_folders"}},
	events.FileCreated:           {Description: "A file appeared in a cloud folder", Fields: []string{"file."}, Filters: []string{"accounts", "search_folders"}},
	events.ScheduleTick:          {Description: "A schedule fired", Fields: []string{"schedule"}, Filters: []string{"schedule"}},
	events.Manual:                {Description: "Started by the user", Fields: []string{"data."}, Filters: []string{"name", "search_folders"}},
	events.Webhook:               {Description: "Incoming webhook call (server mode)", Fields: []string{"webhook."}, Filters: []string{"name"}},
	events.NoReplyTimeout:        {Description: "No reply after the waiting time", Fields: []string{"noreply.", "mail."}, Filters: []string{"accounts", "search_folders"}},
	events.TagAdded:              {Description: "An item got a tag", Fields: []string{"tag.", "item.", "mail."}, Filters: []string{"tags", "search_folders"}},
	events.TagRemoved:            {Description: "A tag was removed from an item", Fields: []string{"tag.", "item.", "mail."}, Filters: []string{"tags", "search_folders"}},
	events.ProjectItemAdded:      {Description: "An item was linked to a project", Fields: []string{"project.", "item.", "mail."}, Filters: []string{"projects", "search_folders"}},
	events.ProjectStatusChanged:  {Description: "A project changed its status", Fields: []string{"project."}, Filters: []string{"projects", "statuses"}},
	events.ProjectDueSoon:        {Description: "An open project is due soon (project.daysLeft)", Fields: []string{"project."}, Filters: []string{"projects"}},
	events.SearchFolderMatched:   {Description: "An item newly matches a search folder", Fields: []string{"searchfolder.", "item.", "mail.", "chat."}, Filters: []string{"search_folders"}},
}

var triggerKeyDocs = []OptionInfo{
	{Path: "event", Type: "enum", Description: "Trigger event (required)"},
	{Path: "accounts", Type: "list", Description: "Account ids (mail, chat, file events)"},
	{Path: "account_names", Type: "list", Description: "Account display names (mail events)"},
	{Path: "folders", Type: "list", Description: "Folder names or roles (mail events)"},
	{Path: "channels", Type: "list", Description: "Messenger channels (chat.received)", Enum: []string{"whatsapp", "signal", "telegram", "matrix"}},
	{Path: "schedule", Type: "string", Description: "Schedule name (schedule.tick)"},
	{Path: "name", Type: "string", Description: "Hook or button name (webhook, manual)"},
	{Path: "tags", Type: "list", Description: "Tag paths incl. descendants (tag.added, tag.removed)"},
	{Path: "projects", Type: "list", Description: "Project keys or names (project.*)"},
	{Path: "statuses", Type: "list", Description: "New project statuses (project.status_changed)", Enum: ProjectStatuses},
	{Path: "search_folders", Type: "list", Description: "searchfolder.matched: the folders; other events: the item must match one of these folders"},
}

type fieldDoc struct {
	desc     string
	examples []string
}

var fieldDocs = map[string]fieldDoc{
	"event":                  {"Trigger event name", []string{`"mail.received"`}},
	"mail.from":              {"Sender address (lower case)", []string{`"chef@firma.de"`}},
	"mail.fromName":          {"Sender display name", []string{`"Anna Meier"`}},
	"mail.fromDomain":        {"Sender domain", []string{`"kunde.de"`}},
	"mail.subject":           {"Subject", []string{`"Rechnung 4711"`}},
	"mail.body":              {"Plain text body", nil},
	"mail.account":           {"Account display name", []string{`"Firma"`}},
	"mail.accountId":         {"Account id", []string{"1"}},
	"mail.folder":            {"Folder name", []string{`"INBOX"`}},
	"mail.oldFolder":         {"Source folder (mail.moved)", nil},
	"mail.category":          {"Smart category", []string{`"rechnung"`, `"newsletter"`}},
	"mail.messageId":         {"Message-ID header", nil},
	"mail.to":                {"To addresses", []string{`"me@firma.de"`}},
	"mail.cc":                {"Cc addresses", nil},
	"mail.bcc":               {"Bcc addresses", nil},
	"mail.recipients":        {"To, Cc and Bcc addresses", nil},
	"mail.attachmentNames":   {"Attachment file names", []string{`"rechnung.pdf"`}},
	"mail.flags":             {"IMAP flags and keywords", []string{`"\\Seen"`}},
	"mail.labels":            {"Labels / tags", []string{`"Kunde"`}},
	"mail.size":              {"Size in bytes", []string{"mb(10)"}},
	"mail.maxAttachmentSize": {"Size of the largest attachment", []string{"mb(5)"}},
	"mail.attachmentCount":   {"Number of attachments", []string{"2"}},
	"mail.isInvoice":         {"Invoice detected (incl. ZUGFeRD/XRechnung)", nil},
	"mail.hasAttachments":    {"Has attachments", nil},
	"mail.pgp.encrypted":     {"OpenPGP encrypted", nil},
	"mail.pgp.signed":        {"OpenPGP signed", nil},
	"mail.pgp.verified":      {"Signature verified", nil},
	"mail.replyAll":          {"Draft created with reply all (mail.before_send)", nil},
	"chat.channel":           {"Messenger channel", []string{`"signal"`, `"whatsapp"`}},
	"chat.chat":              {"Chat title", nil},
	"chat.sender":            {"Sender name or handle", nil},
	"chat.text":              {"Message text", nil},
	"chat.group":             {"Group chat", nil},
	"chat.hasMedia":          {"Has media", nil},
	"calendar.summary":       {"Event title", nil},
	"calendar.location":      {"Event location", nil},
	"calendar.organizer":     {"Organizer address", nil},
	"calendar.attendees":     {"Attendee addresses", nil},
	"calendar.answer":        {"Invitation answer", []string{`"accepted"`, `"declined"`, `"tentative"`}},
	"calendar.minutesBefore": {"Minutes before the start (calendar.event_starting)", []string{"15"}},
	"contact.displayName":    {"Contact name", nil},
	"contact.emails":         {"Contact addresses", nil},
	"contact.groups":         {"Contact groups", nil},
	"file.provider":          {"Cloud provider", []string{`"nextcloud"`}},
	"file.path":              {"File path", nil},
	"file.name":              {"File name", nil},
	"file.size":              {"File size in bytes", nil},
	"noreply.daysWaited":     {"Days since the mail was sent (noreply.timeout)", []string{"3"}},
	"time.hour":              {"Hour (engine time zone)", []string{"8"}},
	"time.weekday":           {"Weekday 1 = Monday … 7 = Sunday", []string{"6"}},
	"time.weekend":           {"Saturday or Sunday", nil},
	"time.workday":           {"Monday to Friday", nil},
	"time.clock":             {`Time of day "15:04"`, []string{`"08:00"`}},
	"item.kind":              {"Kind of the item the event is about", []string{`"message"`, `"chat_message"`}},
	"item.id":                {"Row id of the item (0 if unknown)", nil},
	"item.accountId":         {"Account of the item", nil},
	"tag.name":               {"Tag leaf name (tag.added/removed)", []string{`"Kunde-A"`}},
	"tag.path":               {"Full tag path", []string{`"Projekt/Kunde-A"`}},
	"tag.source":             {"Where the tag came from", []string{`"workflow"`, `"imap_keyword"`}},
	"project.key":            {"Project key", []string{`"HB-2026"`}},
	"project.name":           {"Project name", nil},
	"project.status":         {"Project status", []string{`"active"`}},
	"project.oldStatus":      {"Previous status (project.status_changed)", nil},
	"project.daysLeft":       {"Days until the due date", []string{"3"}},
	"project.customer":       {"Customer / organization", nil},
	"project.tags":           {"Project tags", nil},
	"project.role":           {"Role of the linked item (project.item_added)", nil},
	"project.addedBy":        {"Who linked the item", []string{`"rule"`, `"workflow"`, `"user"`}},
	"searchfolder.name":      {"Search folder name (searchfolder.matched)", nil},
	"searchfolder.query":     {"Search folder query", nil},
}

// extraFields are condition fields beyond the rule fields of rule.go.
var extraFields = []FieldInfo{
	{Name: "item.kind", Kind: KindString}, {Name: "item.id", Kind: KindNumber}, {Name: "item.accountId", Kind: KindNumber},
	{Name: "tag.name", Kind: KindString}, {Name: "tag.path", Kind: KindString}, {Name: "tag.source", Kind: KindString},
	{Name: "project.key", Kind: KindString}, {Name: "project.name", Kind: KindString}, {Name: "project.status", Kind: KindString},
	{Name: "project.oldStatus", Kind: KindString}, {Name: "project.daysLeft", Kind: KindNumber},
	{Name: "project.customer", Kind: KindString}, {Name: "project.tags", Kind: KindList}, {Name: "project.role", Kind: KindString},
	{Name: "project.addedBy", Kind: KindString}, {Name: "searchfolder.name", Kind: KindString}, {Name: "searchfolder.query", Kind: KindString},
}

func init() {
	for _, f := range extraFields {
		f.expr = f.Name
		ruleFields[f.Name] = f
	}
}

var ruleOpDocs = map[string]string{
	"eq": "equals (case-insensitive)", "ne": "not equal", "contains": "contains (case-insensitive)",
	"not_contains": "does not contain", "starts_with": "starts with", "ends_with": "ends with",
	"matches": "regular expression (RE2)", "in": "one of a list", "not_in": "none of a list", "empty": "is empty",
	"not_empty": "is not empty", "domain_is": "address domain is (or subdomain)", "count_gt": "list has more than N entries",
	"count_lt": "list has fewer than N entries", "gt": "greater than", "gte": "greater or equal", "lt": "less than",
	"lte": "less or equal", "is_true": "is true", "is_false": "is false",
}

var functionDocs = []FunctionInfo{
	{Name: "query", Signature: "query(q string) bool", Description: "The item matches the search query (search language)", Example: `query("from:*@kunde.de AND has:attachment")`},
	{Name: "inSearchFolder", Signature: "inSearchFolder(name string) bool", Description: "The item matches a search folder (name or id)", Example: `inSearchFolder("Kunden")`},
	{Name: "domain", Signature: "domain(addr string) string", Description: "Domain of an address", Example: `domain(mail.from) == "kunde.de"`},
	{Name: "icontains", Signature: "icontains(s, sub string) bool", Description: "Case-insensitive contains", Example: `icontains(mail.subject, "rechnung")`},
	{Name: "containsAny", Signature: "containsAny(s string, words []string) bool", Description: "Contains any of the words (case-insensitive)", Example: `containsAny(mail.subject, ["Rechnung", "Invoice"])`},
	{Name: "glob", Signature: "glob(pattern, s string) bool", Description: "Shell pattern match (case-insensitive)", Example: `glob("rechnung*.pdf", .name)`},
	{Name: "anyGlob", Signature: "anyGlob(list []string, pattern string) bool", Description: "Any list entry matches the pattern", Example: `anyGlob(mail.attachmentNames, "*.pdf")`},
	{Name: "kb", Signature: "kb(n int) int", Description: "n kibibytes", Example: "mail.size > kb(500)"},
	{Name: "mb", Signature: "mb(n int) int", Description: "n mebibytes", Example: "mail.maxAttachmentSize > mb(10)"},
	{Name: "any", Signature: "any(list, {predicate}) bool", Description: "Any element matches (# is the element)", Example: `any(mail.attachments, {.size > mb(5)})`},
	{Name: "all", Signature: "all(list, {predicate}) bool", Description: "All elements match", Example: `all(mail.to, {# endsWith "@firma.de"})`},
	{Name: "none", Signature: "none(list, {predicate}) bool", Description: "No element matches"},
	{Name: "len", Signature: "len(v) int", Description: "Length of a string or list", Example: "len(mail.attachments) > 2"},
	{Name: "lower", Signature: "lower(s string) string", Description: "Lower case"},
	{Name: "upper", Signature: "upper(s string) string", Description: "Upper case"},
	{Name: "trim", Signature: "trim(s string) string", Description: "Remove surrounding white space"},
}

var exprOperatorDocs = []OperatorInfo{
	{Name: "and", Kind: "expr", Symbols: []string{"&&", "and"}, Description: "logical and"},
	{Name: "or", Kind: "expr", Symbols: []string{"||", "or"}, Description: "logical or"},
	{Name: "not", Kind: "expr", Symbols: []string{"!", "not"}, Description: "logical not"},
	{Name: "compare", Kind: "expr", Symbols: []string{"==", "!=", "<", "<=", ">", ">="}, Description: "comparison"},
	{Name: "in", Kind: "expr", Symbols: []string{"in", "not in"}, Description: "membership in a list"},
	{Name: "string", Kind: "expr", Symbols: []string{"contains", "startsWith", "endsWith", "matches"}, Description: "string operators (matches: RE2)"},
	{Name: "query-bool", Kind: "query", Symbols: []string{"AND", "OR", "NOT", "&&", "||", "!", "UND", "ODER", "NICHT", "+", "-"}, Description: "search language boolean operators (NOT > AND > OR)"},
	{Name: "query-group", Kind: "query", Symbols: []string{"(", ")"}, Description: "grouping; field:(a OR b) applies a field to a group"},
	{Name: "query-range", Kind: "query", Symbols: []string{"[a TO b]", "{a TO b}", ">", ">=", "<", "<="}, Description: "ranges and comparisons on date:, size:, attachments:"},
	{Name: "query-term", Kind: "query", Symbols: []string{`"phrase"`, `"a b"~5`, "word*", "w?rd", "word~1", "/regex/", "term^2"}, Description: "phrases, proximity, prefix, wildcards, fuzzy, regex, boost"},
}

var optionDocs = []OptionInfo{
	{Path: "options.loop_protection.max_per_message", Type: "int", Default: "3", Description: "Maximum runs per item within the window"},
	{Path: "options.loop_protection.window", Type: "duration", Default: "1h", Description: "Sliding window of the loop protection"},
	{Path: "options.loop_protection.max_depth", Type: "int", Default: "5", Description: "Maximum length of the workflow chain"},
	{Path: "options.rate_limit.max", Type: "int", Default: "60", Description: "Maximum runs per period"},
	{Path: "options.rate_limit.per", Type: "duration", Default: "1m", Description: "Rate limit period"},
	{Path: "options.run_location", Type: "enum", Enum: []string{"", "local", "server"}, Description: "Where the workflow runs (empty = anywhere)"},
	{Path: "options.allow_decrypted_pgp", Type: "bool", Default: "false", Description: "Allow forwarding decrypted PGP content"},
}

var actionKeyDocs = []OptionInfo{
	{Path: "type", Type: "enum", Description: "Action type (required)"},
	{Path: "if", Type: "expr", Description: "Per-action condition; the action is skipped when false"},
	{Path: "continue_on_error", Type: "bool", Default: "false", Description: "Continue with the next action when this one fails"},
}

var placeholderDocs = []PlaceholderInfo{
	{Name: "datum", Alias: "date", Description: "Date of the item (2006-01-02)"},
	{Name: "jahr", Alias: "year", Description: "Year"}, {Name: "monat", Alias: "month", Description: "Month (01)"},
	{Name: "tag", Alias: "day", Description: "Day (02)"}, {Name: "zeit", Alias: "time", Description: "Time (15-04)"},
	{Name: "absender", Alias: "sender", Description: "Sender name, else address"},
	{Name: "absender_email", Alias: "sender_email", Description: "Sender address"},
	{Name: "absender_domain", Alias: "sender_domain", Description: "Sender domain"},
	{Name: "empfaenger", Alias: "recipient", Description: "First recipient"},
	{Name: "betreff", Alias: "subject", Description: "Subject (or event title)"},
	{Name: "konto", Alias: "account", Description: "Account"}, {Name: "ordner", Alias: "folder", Description: "Folder"},
	{Name: "kategorie", Alias: "category", Description: "Category"}, {Name: "kanal", Alias: "channel", Description: "Messenger channel"},
	{Name: "text", Description: "Chat text, else mail body"}, {Name: "ereignis", Alias: "event", Description: "Event name"},
	{Name: "var", Description: "Workflow variable", Example: `{{var "project_key"}}`},
	{Name: "data", Description: "Event data value", Example: `{{data "name"}}`},
	{Name: "header", Description: "Mail header", Example: `{{header "X-Ticket"}}`},
	{Name: "now", Description: "Current time in a layout", Example: `{{now "2006-01-02"}}`},
	{Name: "format_date", Description: "Item date in a layout", Example: `{{format_date "02.01.2006"}}`},
	{Name: "lower", Description: "Lower case", Example: "{{betreff | lower}}"},
	{Name: "upper", Description: "Upper case"}, {Name: "trim", Description: "Trim white space"},
	{Name: "truncate", Description: "Cut to N characters", Example: "{{betreff | truncate 40}}"},
	{Name: "replace", Description: "Replace text", Example: `{{betreff | replace "/" "-"}}`},
	{Name: "default", Description: "Fallback for empty values", Example: `{{kategorie | default "Sonstiges"}}`},
	{Name: "sanitize", Description: "Make a file-name safe segment"},
}

var queryFieldDocs = map[string]string{
	"from": "Sender address or name", "to": "To recipient", "cc": "Cc recipient", "bcc": "Bcc recipient",
	"participant": "Any participant", "subject": "Subject / title", "body": "Text", "attachment": "Attachment name or content",
	"filename": "Attachment file name", "account": "Account (id, name, identity address)", "folder": "Folder (alias in:)",
	"channel": "Channel (mail, whatsapp, signal …)", "type": "Item type", "category": "Smart category",
	"label": "Tag / label (alias tag:)", "lang": "Language", "date": "Date", "size": "Size", "attachments": "Number of attachments",
	"is": "State", "has": "Has …", "exists": "Field exists (alias _exists_:)",
}

// actionParams returns the parameter descriptions of an action type.
func actionParams(at ActionType) []ParamInfo {
	if at.Params != nil {
		return at.Params
	}
	return builtinParams[at.Name]
}

var flagValues = []string{"seen", "read", "flagged", "important", "answered", "forwarded", "junk", "notjunk"}

var builtinParams = map[string][]ParamInfo{
	ActMove: {{Name: "folder", Type: "string", Required: true, Template: true, Positional: 1, Description: "Target folder", Example: "Kunden/Eingang"}},
	ActCopy: {{Name: "folder", Type: "string", Required: true, Template: true, Positional: 1, Description: "Target folder"}},
	ActFlag: {
		{Name: "add", Type: "list", Enum: flagValues, Positional: 1, Variadic: true, Description: "Flags to set (or IMAP keywords)"},
		{Name: "remove", Type: "list", Enum: flagValues, Description: "Flags to clear"},
	},
	ActLabel: {
		{Name: "add", Type: "list", Positional: 1, Variadic: true, Description: "Labels/categories to add"},
		{Name: "remove", Type: "list", Description: "Labels/categories to remove"},
	},
	ActDelete:  {{Name: "permanent", Type: "bool", Default: "false", Description: "Delete permanently instead of moving to trash"}},
	ActArchive: {},
	ActForward: {
		{Name: "to", Type: "list", Required: true, Positional: 1, Variadic: true, Description: "Recipients"},
		{Name: "note", Type: "string", Template: true, Description: "Note above the forwarded mail"},
		{Name: "as_attachment", Type: "bool", Description: "Forward as attachment"},
	},
	ActReplyTemplate: {
		{Name: "body", Type: "string", Required: true, Template: true, Positional: 1, Description: "Reply text"},
		{Name: "subject", Type: "string", Template: true, Description: "Subject (default Re: …)"},
		{Name: "reply_all", Type: "bool", Description: "Reply to all"},
		{Name: "send", Type: "bool", Description: "Send immediately instead of creating a draft"},
	},
	ActReplyAuto: {
		{Name: "body", Type: "string", Required: true, Template: true, Positional: 1, Description: "Reply text"},
		{Name: "subject", Type: "string", Template: true, Description: "Subject"},
	},
	ActUpload: {
		{Name: "target", Type: "string", Required: true, Positional: 1, Description: "Cloud target", Example: "nextcloud"},
		{Name: "dir", Type: "string", Required: true, Template: true, Positional: 2, Description: "Target directory", Example: "/Anhänge/{{jahr}}"},
		{Name: "pattern", Type: "string", Description: "File name pattern", Example: "*.pdf"},
		{Name: "min_size", Type: "size", Description: "Minimum size", Example: "10MB"},
		{Name: "share_link", Type: "bool", Description: "Create a share link"},
		{Name: "share_expiry", Type: "duration", Description: "Link expiry", Example: "14d"},
		{Name: "replace_with_link", Type: "bool", Description: "Replace the attachment by the link (drafts)"},
	},
	ActArchivePDF: {
		{Name: "target", Type: "string", Required: true, Positional: 1, Description: "local, nextcloud, opencloud, webdav or account"},
		{Name: "path", Type: "string", Required: true, Template: true, Positional: 2, Description: "Path incl. file name", Example: "/Buchhaltung/{{jahr}}/{{datum}}_{{absender}}.pdf"},
		{Name: "embed_original", Type: "bool", Default: "true", Description: "Embed the original .eml"},
		{Name: "include_attachments", Type: "bool", Default: "true", Description: "Embed attachments"},
		{Name: "skip_duplicates", Type: "bool", Default: "true", Description: "Skip already archived mails"},
	},
	ActCreateTask: {
		{Name: "title", Type: "string", Required: true, Template: true, Positional: 1, Description: "Task title"},
		{Name: "list", Type: "string", Description: "Task list"},
		{Name: "notes", Type: "string", Template: true, Description: "Notes"},
		{Name: "due_in", Type: "duration", Description: "Due relative to now", Example: "2d"},
	},
	ActCreateEvent: {
		{Name: "title", Type: "string", Required: true, Template: true, Positional: 1, Description: "Event title"},
		{Name: "calendar", Type: "string", Description: "Calendar"},
		{Name: "notes", Type: "string", Template: true, Description: "Notes"},
		{Name: "location", Type: "string", Template: true, Description: "Location"},
		{Name: "start_in", Type: "duration", Description: "Start relative to now", Example: "1d"},
		{Name: "duration", Type: "duration", Default: "1h", Description: "Duration"},
	},
	ActNotify: {
		{Name: "channel", Type: "string", Positional: 1, Default: "desktop", Enum: []string{"desktop", "push", "signal:me", "telegram:me", "matrix:me"},
			Description: "desktop, push or <messenger>:<target>; in the rule syntax a bare messenger name means <name>:me"},
		{Name: "text", Type: "string", Required: true, Template: true, Positional: 2, Description: "Message text", Example: "Neue Kundenmail: {{betreff}}"},
		{Name: "title", Type: "string", Template: true, Description: "Title"},
	},
	ActWebhook: {
		{Name: "url", Type: "string", Required: true, Positional: 1, Description: "http(s) URL"},
		{Name: "method", Type: "enum", Enum: []string{"POST", "PUT"}, Default: "POST", Description: "HTTP method"},
		{Name: "headers", Type: "map", Description: "Extra headers"},
		{Name: "body", Type: "string", Template: true, Description: "Body template (default: JSON document)"},
		{Name: "timeout", Type: "duration", Default: "10s", Description: "Timeout (max 2m)"},
		{Name: "include_body", Type: "bool", Description: "Include the mail/chat text in the JSON document"},
	},
	ActSetVar: {
		{Name: "name", Type: "string", Required: true, Positional: 1, Description: "Variable name"},
		{Name: "value", Type: "string", Template: true, Positional: 2, Description: "Value (may be empty)"},
	},
	ActRunWorkflow:   {{Name: "id", Type: "string", Required: true, Positional: 1, Description: "Workflow id"}},
	ActRemindNoReply: {{Name: "after", Type: "duration", Required: true, Positional: 1, Description: "Waiting time", Example: "3d"}},
	ActVacation:      {},
	ActWarn: {
		{Name: "message", Type: "string", Positional: 1, Description: "Warning (English; message or message_de is required)"},
		{Name: "message_de", Type: "string", Description: "Warning (German)"},
		{Name: "key", Type: "string", Description: "Stable key of the finding"},
	},
	ActBlock: {
		{Name: "message", Type: "string", Positional: 1, Description: "Reason (English; message or message_de is required)"},
		{Name: "message_de", Type: "string", Description: "Reason (German)"},
		{Name: "key", Type: "string", Description: "Stable key of the finding"},
	},
}

// conditionFields returns all condition fields sorted by name.
func conditionFields() []FieldInfo { return RuleFields() }

// Describe returns the description of the workflow language for the
// actions in reg (DefaultRegistry if nil).
func Describe(reg *Registry) Description {
	if reg == nil {
		reg = DefaultRegistry()
	}
	d := Description{Version: DescriptionVersion, TriggerKeys: triggerKeyDocs, Functions: functionDocs, Options: optionDocs,
		ActionKeys: actionKeyDocs, Placeholders: placeholderDocs, RuleKeywords: ruleKeywords, ScopeKeys: ruleScopeKeys}
	for _, t := range events.AllTypes {
		ti := triggerDocs[t]
		ti.Event = string(t)
		if ti.Fields == nil {
			ti.Fields = []string{}
		}
		ti.Filters = append([]string{}, ti.Filters...)
		d.Triggers = append(d.Triggers, ti)
	}
	for _, f := range conditionFields() {
		fd := FieldDesc{Name: f.Name, Type: string(f.Kind), Operators: RuleOps[f.Kind]}
		if doc, ok := fieldDocs[f.Name]; ok {
			fd.Description, fd.Examples = doc.desc, doc.examples
		}
		d.Fields = append(d.Fields, fd)
	}
	for _, p := range []string{"mail.headers.", "vars.", "data.", "webhook.headers."} {
		d.DynamicField = append(d.DynamicField, FieldDesc{Name: p + "<name>", Type: string(KindString), Operators: RuleOps[KindString],
			Description: "Value of a " + strings.TrimSuffix(p, ".") + " entry"})
	}
	var ops []string
	for op := range ruleOpDocs {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		var types []string
		for _, k := range []FieldKind{KindString, KindList, KindNumber, KindBool} {
			if contains(RuleOps[k], op) {
				types = append(types, string(k))
			}
		}
		d.Operators = append(d.Operators, OperatorInfo{Name: op, Kind: "rule", Types: types, Description: ruleOpDocs[op]})
	}
	d.Operators = append(d.Operators, exprOperatorDocs...)
	for _, at := range reg.Types() {
		ai := ActionInfo{Type: at.Name, Description: at.Description, ExternalEffect: at.ExternalEffect, Params: actionParams(at)}
		if ai.Params == nil {
			ai.Params = []ParamInfo{}
		}
		for _, t := range at.Triggers {
			ai.Triggers = append(ai.Triggers, string(t))
		}
		d.Actions = append(d.Actions, ai)
	}
	d.QueryFields = queryFields()
	return d
}

func queryFields() []QueryFieldInfo {
	var out []QueryFieldInfo
	for _, f := range query.Fields {
		qi := QueryFieldInfo{Name: f, Description: queryFieldDocs[f], InMemory: f != "lang"}
		switch f {
		case "is":
			qi.Values = query.IsValues
		case "has":
			qi.Values = query.HasValues
		case "exists":
			qi.Values = query.ExistsValues
		case "type":
			qi.Values = []string{"mail", "chat", "event", "task", "contact", "file"}
		case "channel":
			qi.Values = []string{store.ChannelMail, store.ChannelWhatsApp, store.ChannelSignal, store.ChannelTelegram, store.ChannelMatrix}
		case "date":
			qi.Values, qi.Ranges = []string{"today", "yesterday", "now-7d", `"last week"`, "this month", "heute", "gestern", `"letzte Woche"`}, true
		case "size":
			qi.Values, qi.Ranges = []string{">1MB", ">10MB", "<100k"}, true
		case "attachments":
			qi.Values, qi.Ranges = []string{">0", ">2"}, true
		case "folder":
			qi.Values = []string{"inbox", "sent", "drafts", "archive", "trash", "spam"}
		}
		out = append(out, qi)
	}
	out = append(out, QueryFieldInfo{Name: "header.<Name>", Description: "Mail header value", InMemory: true})
	return out
}
