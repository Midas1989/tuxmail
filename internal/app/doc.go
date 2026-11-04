// SPDX-License-Identifier: AGPL-3.0-or-later

// Package app is TuxMail's application layer (KONZEPT 4.1): it implements
// the frontend contract frontend/src/lib/api/api.ts on top of the store and
// the core services, for one user. The same App serves
//
//   - the desktop build through Wails bindings: Api (package app, struct
//     Api, so the frontend calls window.go.app.Api.<Method>; see api.go),
//     events are emitted as the runtime event "tuxmail:event";
//   - the server/web mode through internal/server/api (HTTP/JSON + the
//     WebSocket /api/events), one App per user on
//     data_dir/users/<id>/tuxmail.db.
//
// Areas: accounts.go (accounts, identities, persons, folders), messages.go
// (classic list, detail, attachments, move/flags), conversations.go and
// items.go (thread and person grouping, hybrid scopes with inScope, chat
// bubbles), chatfeatures.go (chat folders, drafts, scheduled messages,
// uploads, snippets, local tasks/events, meeting proposals), search.go,
// organize.go (tags, projects, search folders), workflows.go, settings.go
// (preferences, locales, notifications, Web Push passthrough, vault),
// ai.go (OpenAI-compatible/Anthropic/Gemini via internal/ai),
// provisioning.go (desktop: connect to a TuxMail server), pim*.go
// (calendars, tasks, contacts, invitations, CalDAV/CardDAV sync; see
// pim.go). Additional state
// lives in migration store/migrations/0005_app.sql (store/appstate.go,
// store/appviews.go).
//
// # Wiring points
//
// Sync (internal/sync, step A): Options.Sync creates a sync.Manager with
// the store, blobs, bus, secrets, the project service as ProjectHooks and
// Tagger, ParseExtractor (internal/mime/parse) as content extractor and the
// send actions as workflow Sender; actions (Move, SetFlags, Delete) go
// through Syncer (managerSyncer), which applies them locally first and
// queues them for the server. The manager's UI topics (account.status,
// conversation.updated, message.updated, folder.updated, folders.changed,
// op.failed) are mapped to ApiEvents in publishSyncEvent; mails without
// body are fetched on demand (Manager.FetchBody). Without Sync, localSyncer
// changes only the local store. Threads come from the sync threading
// engine; ensureThread only covers mails without conversation.
//
// Send (internal/send, step B): Options.Send creates a MailSender with
// send.SMTPFactory, the store (identities, cloud links), the bus (hooks,
// events), followup.Scheduler, send.StoreQueue and the adapters in
// sender.go (AppendSent and SaveDraft via sync Actions.Append, MarkOriginal
// via Syncer.SetFlags, LoadRaw/OpenBlob via the blob store, relations via
// the threading engine). Delayed sends of internal/send (ops of kind
// send.OpKind, account 0) are executed by runSendOps; chat-mode scheduled
// messages are kept in scheduled_messages and sent when due. Send-check
// blocks are returned as SendResult{blocked, findings}; too large mails as
// error code "too_large". The built-in checks (workflow/sendcheck) run as
// mail.before_send hook; composer sends also wait for warnings to be
// confirmed (OutgoingMessage.ConfirmChecks), due scheduled messages do not.
//
// Rendering (internal/mime/parse + internal/mime/sanitize): MIMERenderer
// (default) sanitizes HTML with cid URLs /api/messages/{id}/cid/{cid} and
// the remote-image proxy /api/proxy/image?url= (blocked for now);
// bubbles use internal/mime/quote. BasicRenderer is a dependency-free
// fallback that never returns HTML.
//
// Messenger plugins: chats and chat messages are read from the store;
// sending to messenger channels returns not_supported until a plugin
// Sender exists.
//
// Cloud files (KONZEPT 12, cloud_*.go on internal/cloud/...): cloud
// accounts are accounts rows of kind nextcloud, opencloud, vaultwarden or
// webdav (Extra: server, user; the secret store keeps the app password,
// OAuth token or Vaultwarden session). StartCloudLogin/PollCloudLogin run
// Nextcloud Login Flow v2 and OpenID Connect (loopback redirect on the
// desktop, /api/cloud/oauth/callback in server mode); Vaultwarden and
// WebDAV log in directly. cloudUploader is the send.CloudUploader: every
// attachment of the composer picks its own provider (delivery nextcloud,
// opencloud, vaultwarden -> send.CloudOptions.FileAccounts), files go to
// <uploadDir>/<date>/<subject>/ with a policy compliant password, links are
// recorded in cloud_links (+ cloud_link_meta, migration 0007_cloud.sql) and
// listed/revoked as "My shares". workflowCloud and workflowPDF implement
// workflow.CloudActions and PDFActions (duplicate protection via
// cloud_archive); the PDF itself comes from SetPDFRenderer.
package app
