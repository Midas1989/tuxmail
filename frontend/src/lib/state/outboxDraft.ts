// SPDX-License-Identifier: AGPL-3.0-or-later
//
// A mail from the outbox (desktop app or Android app in server mode) back in
// the composer, with the reason it did not go out. The copy stays in the
// outbox until it is sent or discarded.

import { i18n } from '../i18n/index.svelte';
import type { OutboxEntry, OutgoingMessage } from '../types';
import type { ComposerInit } from './app.svelte';

export function outboxReasons(o: OutboxEntry): string[] {
  return (o.findings ?? []).map((f) => f.localized?.[i18n.locale] ?? f.message);
}

function escapeHtml(text: string): string {
  return (text ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/\n/g, '<br>');
}

export async function openOutboxDraft(conn: { outboxMessage(id: string): Promise<OutgoingMessage> }, app: { openCompose(init: ComposerInit): void }, o: OutboxEntry): Promise<void> {
  const m = await conn.outboxMessage(o.id);
  app.openCompose({
    mode: 'new',
    identityId: m.identityId,
    to: m.to,
    cc: m.cc,
    subject: m.subject,
    bodyHtml: m.html || `<p>${escapeHtml(m.text)}</p>`,
    inReplyToMessageId: m.inReplyToMessageId,
    forwardOfMessageId: m.forwardOfMessageId,
    outbox: { id: o.id, reasons: outboxReasons(o), bcc: m.bcc, attachments: m.attachments },
  });
}
