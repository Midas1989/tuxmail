// SPDX-License-Identifier: AGPL-3.0-or-later

// Package threading implements the conversation mapping of TuxMail
// (docs/KONZEPT.md, chapter 6).
//
// The package is pure Go and has no database dependency. It consists of:
//
//   - stable message keys: Message-ID normalization, parsing of
//     References/In-Reply-To header values, server object IDs and a
//     fingerprint fallback (6.2);
//   - relation extraction from headers, server thread hints, Outlook
//     Thread-Index, compose actions, attached messages, iCalendar UIDs and
//     inline-forwarded messages (6.3);
//   - subject normalization for heuristic matching;
//   - an incremental Engine that assigns every message to exactly one
//     conversation using union-find over the stored relations (6.4),
//     persisting through the small Repository interface.
//
// # Keys
//
// Relations are keyed by message key, never by database row. A message key
// is the normalized Message-ID ("abc@example.com"). Messages without a
// Message-ID use "~obj:<ns>:<id>" (server object ID) or "~fp:<sha256>"
// (fingerprint). Relations may also point at virtual keys that represent a
// thread rather than a message: "~thread:<ns>:<id>" (server thread ID),
// "~tidx:<hex>" (Outlook Thread-Index root) and "~ical:<uid>" (iCalendar
// UID). Virtual keys are ordinary nodes in the union-find graph; they never
// become members of a conversation themselves. The "~" prefix cannot clash
// with a normalized Message-ID in practice.
package threading
