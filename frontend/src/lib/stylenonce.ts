// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// srcdoc documents inherit the Content-Security-Policy of the page, so on the web
// client their <style> elements need the page's style nonce (internal/server,
// contentSecurityPolicy). Without a server (Wails, Android) there is no such meta tag
// and nothing to do.

export function styleNonce(): string | undefined {
  if (typeof document === 'undefined') return undefined;
  return document.querySelector<HTMLMetaElement>('meta[name="tuxmail-style-nonce"]')?.content || undefined;
}

/** Puts the nonce on all <style> elements of a srcdoc document. */
export function withStyleNonce(html: string, nonce: string | undefined = styleNonce()): string {
  return nonce ? html.replace(/<style\b/gi, `<style nonce="${nonce}"`) : html;
}
