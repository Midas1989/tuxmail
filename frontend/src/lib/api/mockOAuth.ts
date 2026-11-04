// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Mock of the OAuth / cloud account API (Microsoft 365, Google): sign-in
// flows finish after a short delay, own client IDs are kept in memory,
// shared mailboxes become accounts with a delegated identity.

import { ApiError } from './api';
import type {
  Account,
  CloudPimResult,
  Identity,
  IdentityDelegation,
  NewAccountInput,
  OAuthClientInput,
  OAuthFlowStatus,
  OAuthProviderId,
  OAuthProviderInfo,
  OAuthStartInput,
  SharedMailboxInput,
} from '../types';

export interface MockOAuthHost {
  accounts(): Account[];
  identities(): Identity[];
  createAccount(input: NewAccountInput): Promise<Account>;
}

/** Sign-in service of a mail domain (mock autoconfig). */
export function mockOAuthProvider(domain: string): OAuthProviderId | undefined {
  if (domain === 'gmail.com' || domain === 'googlemail.com') return 'google';
  if (['outlook.com', 'hotmail.com', 'live.com', 'outlook.de', 'hotmail.de'].includes(domain)) return 'microsoft';
  return undefined;
}

export class MockOAuth {
  /** Delay until a mock sign-in completes. */
  flowDelayMs = 1500;
  private clients: Partial<Record<OAuthProviderId, OAuthClientInput>> = {};
  /** Microsoft is "configured by the environment" in the demo, Google is not. */
  private env: Partial<Record<OAuthProviderId, string>> = { microsoft: '00000000-demo-4c0b-9a1e-tuxmail' };
  private flows = new Map<string, OAuthFlowStatus>();
  private delegations: IdentityDelegation[] = [];
  private seq = 0;

  constructor(private readonly host: MockOAuthHost) {}

  providers(): OAuthProviderInfo[] {
    const mk = (id: OAuthProviderId, displayName: string, kinds: OAuthProviderInfo['kinds'], deviceFlow: boolean): OAuthProviderInfo => {
      const own = this.clients[id];
      const envId = this.env[id];
      return {
        id,
        displayName,
        configured: !!(own?.clientId || envId),
        source: own ? 'user' : envId ? 'env' : '',
        clientId: own?.clientId ?? envId,
        tenant: own?.tenant,
        userClient: own ? { ...own, clientSecret: undefined, hasSecret: !!own.clientSecret || own.hasSecret } : undefined,
        allowUserClients: true,
        deviceFlow,
        kinds,
      };
    };
    return [mk('microsoft', 'Microsoft', ['graph', 'imap', 'eas', 'ews'], true), mk('google', 'Google', ['gmail', 'imap'], false)];
  }

  saveClient(input: OAuthClientInput): void {
    if (!input.clientId.trim()) throw new ApiError('client ID required', 400, 'invalid_input');
    const prev = this.clients[input.provider];
    this.clients[input.provider] = { ...input, hasSecret: !!input.clientSecret || (input.hasSecret && !!prev) };
  }

  deleteClient(provider: string): void {
    delete this.clients[provider as OAuthProviderId];
  }

  start(input: OAuthStartInput): OAuthFlowStatus {
    const p = this.providers().find((x) => x.id === input.provider);
    if (!p) throw new ApiError(`unknown provider ${input.provider}`, 400, 'invalid_input');
    if (!p.configured) {
      throw new ApiError('no OAuth client ID is configured for this provider (Settings → Sign-in services)', 409, 'oauth_not_configured');
    }
    const id = `oa_mock${++this.seq}`;
    const mode = input.mode ?? 'loopback';
    const st: OAuthFlowStatus = {
      id,
      provider: input.provider,
      mode,
      status: 'pending',
      email: input.email,
      authUrl: `https://login.example/${input.provider}/authorize?login_hint=${encodeURIComponent(input.email)}`,
      expiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
    };
    if (mode === 'device') {
      st.userCode = 'TUX-MAIL';
      st.verificationUri = 'https://microsoft.com/devicelogin';
    }
    this.flows.set(id, st);
    setTimeout(() => void this.finish(id, input), this.flowDelayMs);
    return { ...st };
  }

  private async finish(id: string, input: OAuthStartInput): Promise<void> {
    const st = this.flows.get(id);
    if (!st || st.status !== 'pending') return;
    if (input.email.toLowerCase().startsWith('denied')) {
      Object.assign(st, { status: 'error', errorCode: 'oauth_consent_required', error: 'an administrator of the organization must approve TuxMail' });
      return;
    }
    const acc = await this.host.createAccount({ kind: input.kind, displayName: input.displayName || input.email, email: input.email, name: input.name || input.email });
    acc.extra = { ...acc.extra, oauthProvider: input.provider };
    acc.incoming = undefined;
    Object.assign(st, { status: 'done', accountId: acc.id });
  }

  flow(id: string): OAuthFlowStatus {
    const st = this.flows.get(id);
    if (!st) throw new ApiError('not found', 404, 'not_found');
    return { ...st };
  }

  cancel(id: string): void {
    const st = this.flows.get(id);
    if (st && st.status === 'pending') st.status = 'canceled';
  }

  async addShared(input: SharedMailboxInput): Promise<Account> {
    const owner = this.host.accounts().find((a) => a.id === input.accountId);
    if (!owner) throw new ApiError('not found', 404, 'not_found');
    if (!/^[^\s@]+@[^\s@]+$/.test(input.address)) throw new ApiError('invalid e-mail address', 400, 'invalid_input');
    const acc = await this.host.createAccount({ kind: owner.kind, displayName: input.displayName || input.address, email: input.address, name: input.displayName || input.address });
    acc.extra = { ...acc.extra, mailbox: input.address, sendMode: input.sendMode ?? '', ownerAccountId: String(owner.id) };
    const ident = this.host.identities().find((i) => i.accountId === acc.id);
    if (ident && input.sendMode) this.delegations.push({ identityId: ident.id, mode: input.sendMode, mailbox: input.address, ownerAccountId: owner.id });
    return acc;
  }

  listDelegations(): IdentityDelegation[] {
    return this.delegations.map((d) => ({ ...d }));
  }

  setDelegation(d: { identityId: number; mode: string; mailbox?: string }): void {
    this.delegations = this.delegations.filter((x) => x.identityId !== d.identityId);
    if (d.mode === 'send_as' || d.mode === 'send_on_behalf') {
      const ident = this.host.identities().find((i) => i.id === d.identityId);
      this.delegations.push({ identityId: d.identityId, mode: d.mode, mailbox: d.mailbox || ident?.email || '' });
    }
  }

  syncPim(accountId: number): CloudPimResult {
    if (!this.host.accounts().some((a) => a.id === accountId)) throw new ApiError('not found', 404, 'not_found');
    return { calendars: 1, events: 12, addressBooks: 1, contacts: 34, tasks: 5, deleted: 0 };
  }
}
