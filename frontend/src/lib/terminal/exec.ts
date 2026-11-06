// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Runs the command line of the terminal view through the same state and API
// calls as the other views. No shell, no PTY, no new server endpoint.

import type { Api, MessageScope } from '../api/api';
import type { ComposerInit, SidebarSel } from '../state/app.svelte';
import type { Address, Folder, MessageDetail, MessageSummary } from '../types';
import { FLAG_FLAGGED, FLAG_SEEN } from '../types';
import { t } from '../i18n/index.svelte';
import { ParseError, ROLES, UNIFIED, cleanLine, parseLine, specs } from './parse';

/** What the commands need from the application state (AppState implements it). */
export interface TermHost {
  readonly api: Api;
  readonly folders: Record<number, Folder[]>;
  readonly selectedMessageId: number | null;
  readonly message: MessageDetail | null;
  select(sel: SidebarSel): Promise<void>;
  selectMessage(id: number | null): Promise<void>;
  openCompose(init?: ComposerInit): void;
  replyInit(m: MessageDetail, mode: 'reply' | 'replyAll', bodyHtml?: string): ComposerInit;
  setViewMode(mode: 'classic'): Promise<void>;
}

export type OutLine = { kind: 'text' | 'error'; text: string } | { kind: 'row'; id: number; text: string };

export class TermError extends Error {
  constructor(
    readonly key: 'notFound' | 'ambiguous' | 'noSelection',
    readonly detail = '',
  ) {
    super(key);
  }
}

function folderPath(f: Folder): string {
  return cleanLine(f.delimiter && f.remoteId ? f.remoteId.split(f.delimiter).join('/') : f.name);
}

/** Same lookup order as mailcmd.findFolder: id, role, full path, last name. */
export function findFolder(all: Folder[], ref: string): Folder {
  if (/^[0-9]+$/.test(ref)) {
    const byId = all.find((f) => f.id === Number(ref));
    if (byId) return byId;
  }
  const lower = ref.toLowerCase();
  const matchers: ((f: Folder) => boolean)[] = [
    (f) => ROLES.includes(lower) && f.role === lower,
    (f) => folderPath(f).toLowerCase() === lower,
    (f) => (folderPath(f).split('/').pop() ?? '').toLowerCase() === lower,
  ];
  for (const match of matchers) {
    const hits = all.filter(match);
    if (hits.length === 1) return hits[0];
    if (hits.length > 1) throw new TermError('ambiguous', hits.map((f) => `${f.id} (${folderPath(f)})`).join(', '));
  }
  throw new TermError('notFound', cleanLine(ref));
}

function hostFolders(host: TermHost, account?: number): Folder[] {
  return Object.values(host.folders)
    .flat()
    .filter((f) => account == null || f.accountId === account);
}

function addressText(a: Address | undefined): string {
  if (!a) return '';
  const name = cleanLine(a.name ?? '');
  const email = cleanLine(a.email);
  return name ? `${name} <${email}>` : email;
}

export function rowText(m: MessageSummary): string {
  const from = addressText(m.from[0]);
  return `${m.date.slice(0, 10)}  ${from}  ${cleanLine(m.subject)}`;
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/\n/g, '<br>');
}

function addresses(list: unknown): Address[] {
  return ((list as string[] | undefined) ?? []).map((s) => {
    const m = /^\s*(.*?)\s*<([^<>]+)>\s*$/.exec(s);
    return m ? { name: m[1], email: m[2] } : { email: s.trim() };
  });
}

function targets(ids: unknown, host: TermHost): number[] {
  const list = ids as number[] | undefined;
  if (list?.length) return list;
  if (host.selectedMessageId != null) return [host.selectedMessageId];
  throw new TermError('noSelection');
}

const FLAG_CHANGE: Record<string, { add: string[]; remove: string[] }> = {
  read: { add: [FLAG_SEEN], remove: [] },
  unread: { add: [], remove: [FLAG_SEEN] },
  flagged: { add: [FLAG_FLAGGED], remove: [] },
  unflagged: { add: [], remove: [FLAG_FLAGGED] },
};

/** Commands and their usage, for `help`. */
export function usageLines(): string[] {
  return specs.map((s) => s.usage);
}

/** Executes one line; returns the lines for the output area. */
export async function runCommand(line: string, host: TermHost): Promise<OutLine[]> {
  const cmd = parseLine(line);
  const a = cmd.args;
  const f = cmd.flags;
  const account = f.account as number | undefined;
  switch (cmd.name) {
    case 'help':
      return [...usageLines(), t('mail.terminal.keysHint')].map((text) => ({ kind: 'text', text }));
    case 'quit':
      await host.setViewMode('classic');
      return [];
    case 'folders':
      return hostFolders(host, account).map((x) => ({ kind: 'text', text: `${x.id}  ${x.role ?? '-'}  ${x.unread}/${x.total}  ${folderPath(x)}` }));
    case 'list': {
      const ref = (a.folder as string | undefined) ?? '';
      if (ref === '' && account == null) {
        await host.select({ kind: 'unified', role: 'inbox' });
      } else if (UNIFIED.includes(ref.toLowerCase())) {
        await host.select({ kind: 'unified', role: ref.toLowerCase() as 'unread' | 'flagged' });
      } else {
        const x = findFolder(hostFolders(host, account), ref || 'inbox');
        await host.select({ kind: 'folder', accountId: x.accountId, folderId: x.id });
      }
      return [];
    }
    case 'search': {
      let scope: MessageScope = { kind: 'all' };
      if (f.folder != null) scope = { kind: 'folder', folderId: findFolder(hostFolders(host, account), f.folder as string).id };
      const page = await host.api.listMessages({ scope, search: a.query as string, offset: (f.offset as number) ?? 0, limit: (f.limit as number) ?? 50 });
      return page.items.map((m) => ({ kind: 'row', id: m.id, text: rowText(m) }));
    }
    case 'read':
      await host.selectMessage(targets(a.id, host)[0]);
      return [];
    case 'reply': {
      const id = targets(a.id, host)[0];
      if (host.selectedMessageId !== id || !host.message) await host.selectMessage(id);
      const m = host.message;
      if (!m) throw new TermError('notFound', String(id));
      const body = f.text ? escapeHtml(f.text as string) : '';
      host.openCompose(host.replyInit(m, f.all ? 'replyAll' : 'reply', body));
      return [];
    }
    case 'send':
      // The interactive views always show the message before it is sent.
      host.openCompose({
        mode: 'new',
        identityId: f.identity as number | undefined,
        to: addresses(f.to),
        cc: addresses(f.cc),
        subject: f.subject as string | undefined,
        bodyHtml: f.text ? escapeHtml(f.text as string) : undefined,
      });
      return [];
    case 'mark': {
      const ids = targets(a.ids, host);
      const change = FLAG_CHANGE[a.state as string];
      await host.api.setFlags(ids, change.add as never, change.remove as never);
      return [{ kind: 'text', text: t('mail.terminal.marked', { ids: ids.join(','), state: a.state as string }) }];
    }
    case 'move': {
      const ids = targets(a.ids, host);
      const first = await host.api.getMessage(ids[0]);
      const x = findFolder(hostFolders(host, account ?? first.accountId), a.folder as string);
      await host.api.move(ids, x.id);
      if (host.selectedMessageId != null && ids.includes(host.selectedMessageId)) await host.selectMessage(null);
      return [{ kind: 'text', text: t('mail.terminal.moved', { ids: ids.join(','), folder: folderPath(x) }) }];
    }
  }
  throw new ParseError('unknown_command', cmd.name);
}
