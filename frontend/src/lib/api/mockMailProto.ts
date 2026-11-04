// SPDX-License-Identifier: AGPL-3.0-or-later
// Mock of remote content and server rules (Sieve) for the demo transport.

import { ApiError } from './api';
import { restoreRemoteImages } from '../remote';
import { lintSieve } from '../sieve';
import type { RemoteAllowEntry, RemoteContent, SieveCheckResult, SieveScript, SieveStatus } from '../types';

const DEMO_SCRIPT = `require ["fileinto", "imap4flags"];

# Mails vom Chef markieren
if address :is "from" "chef@firma.example" {
  addflag "\\\\Flagged";
}

# Newsletter
if exists "list-id" {
  fileinto "Newsletter";
}
`;

interface AccountRules {
  scripts: Map<string, string>;
  active: string;
  syncWorkflows: boolean;
}

export class MockMailProto {
  private readonly allowList: RemoteAllowEntry[] = [];
  private readonly rules = new Map<number, AccountRules>();

  private acc(accountId: number): AccountRules {
    let r = this.rules.get(accountId);
    if (!r) {
      r = { scripts: new Map([['roundcube', DEMO_SCRIPT]]), active: 'roundcube', syncWorkflows: false };
      this.rules.set(accountId, r);
    }
    return r;
  }

  remoteContent(html: string): RemoteContent {
    // The demo has no proxy: remote images are restored directly.
    return { html: restoreRemoteImages(html) };
  }

  listAllow(): RemoteAllowEntry[] {
    return this.allowList;
  }
  allow(kind: 'sender' | 'domain', value: string): void {
    const v = value.trim().toLowerCase().replace(/^@/, '');
    if (!v || (kind === 'sender' && !v.includes('@'))) throw new ApiError('invalid allowlist entry', 400, 'invalid_input');
    if (!this.allowList.some((e) => e.kind === kind && e.value === v)) this.allowList.push({ kind, value: v, createdAt: new Date().toISOString() });
  }
  removeAllow(kind: 'sender' | 'domain', value: string): void {
    const i = this.allowList.findIndex((e) => e.kind === kind && e.value === value.toLowerCase());
    if (i >= 0) this.allowList.splice(i, 1);
  }
  allowed(sender: string): boolean {
    const s = sender.toLowerCase();
    const dom = s.split('@')[1] ?? '';
    return this.allowList.some((e) => (e.kind === 'sender' ? e.value === s : dom === e.value || dom.endsWith('.' + e.value)));
  }

  status(accountId: number): SieveStatus {
    const r = this.acc(accountId);
    return {
      accountId,
      available: true,
      extensions: ['fileinto', 'copy', 'imap4flags', 'regex', 'vacation', 'date', 'relational', 'include'],
      scripts: [...r.scripts.keys()].map((name) => ({ name, active: name === r.active })),
      syncWorkflows: r.syncWorkflows,
      included: r.syncWorkflows ? ['newsletter-archivieren'] : [],
      skipped: [{ id: 'rechnungen-nextcloud', name: 'Rechnungen als PDF in Nextcloud', reason: 'action archive_pdf runs only in TuxMail' }],
    };
  }
  script(accountId: number, name: string): SieveScript {
    const r = this.acc(accountId);
    const src = r.scripts.get(name);
    if (src === undefined) throw new ApiError('no such script', 404, 'not_found');
    return { name, script: src, active: r.active === name, parse: { commands: [], requires: [], managed: src.includes('# BEGIN TuxMail') } };
  }
  check(script: string): SieveCheckResult {
    const l = lintSieve(script);
    return l.error ? { ok: false, error: `line ${l.line}: ${l.error}`, line: l.line } : { ok: true };
  }
  put(accountId: number, name: string, script: string, activate: boolean): void {
    const c = this.check(script);
    if (!c.ok) throw new ApiError(c.error ?? 'invalid script', 409, 'invalid_input');
    const r = this.acc(accountId);
    r.scripts.set(name, script);
    if (activate) r.active = name;
  }
  activate(accountId: number, name: string): void {
    const r = this.acc(accountId);
    if (name && !r.scripts.has(name)) throw new ApiError('no such script', 404, 'not_found');
    r.active = name;
  }
  remove(accountId: number, name: string): void {
    const r = this.acc(accountId);
    if (r.active === name) throw new ApiError('the active script cannot be deleted', 409, 'invalid_input');
    r.scripts.delete(name);
  }
  setWorkflowSync(accountId: number, enabled: boolean): SieveStatus {
    const r = this.acc(accountId);
    r.syncWorkflows = enabled;
    const src = r.scripts.get(r.active) ?? '';
    const stripped = src.replace(/# BEGIN TuxMail[\s\S]*?# END TuxMail\n?/, '');
    r.scripts.set(
      r.active || 'tuxmail',
      enabled
        ? `# BEGIN TuxMail (managed by TuxMail; changes inside this section are overwritten)\nrequire ["fileinto"];\n# BEGIN part rules\n# workflow newsletter-archivieren: Newsletter archivieren\nif exists "list-id" {\n  fileinto "Archiv/Newsletter";\n}\n# END part rules\n# END TuxMail\n${stripped}`
        : stripped,
    );
    if (!r.active) r.active = 'tuxmail';
    return this.status(accountId);
  }
}
