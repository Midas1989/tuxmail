// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sanitize turns untrusted HTML mail bodies into markup that is safe
// to display in a sandboxed iframe (docs/KONZEPT.md 8.3 and 17.1).
//
// Sanitize works in two passes. A structural pre-pass over the parsed DOM
// (golang.org/x/net/html) rewrites resource references: remote images and
// CSS url() values are blocked by default (replaced by a transparent
// placeholder and remembered in data-remote-src) or routed through a
// configurable proxy, cid: references are rewritten to a resolver URL,
// tracking pixels are detected and removed, <style> blocks are extracted and
// filtered, and links are analysed for phishing hints (display text naming a
// different domain than the target). The result is then run through a
// bluemonday allowlist policy (no scripts, forms, event handlers, frames,
// objects; a safe CSS subset) and, unless a fragment is requested, wrapped in
// a complete document carrying a strict Content-Security-Policy.
//
// ToText converts HTML to readable plain text (used for the plain-text
// alternative of composed mails and for snippets).
package sanitize
