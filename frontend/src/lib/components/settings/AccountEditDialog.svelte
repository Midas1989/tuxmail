<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  "Konto bearbeiten" (Settings -> Konten): general settings and status,
  server and sign-in, identities/aliases, folders, index policy and
  diagnostics of one account (Go: internal/app/account_edit.go).
-->
<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import PlugZap from '@lucide/svelte/icons/plug-zap';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import CircleX from '@lucide/svelte/icons/circle-x';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import Trash from '@lucide/svelte/icons/trash';
  import Download from '@lucide/svelte/icons/download';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import LogIn from '@lucide/svelte/icons/log-in';
  import { app } from '../../state';
  import { t, fmt, type MessageKey } from '../../i18n/index.svelte';
  import { ApiError } from '../../api';
  import { MAIL_KINDS, type AccountSettings, type AccountUpdate, type AuthMethod, type BodyPolicy, type ConnectionTestResult, type IdleMode, type OAuthProviderId, type Pop3Options, type ServerConfig, type SieveConfig, type TLSMode } from '../../types';
  import Modal from '../common/Modal.svelte';
  import FolderSettings from './FolderSettings.svelte';
  import AccountIdentities from './AccountIdentities.svelte';
  import OAuthSignIn from './OAuthSignIn.svelte';
  import { defaultPort } from './AccountWizard.svelte';
  import { isPlainHttpUrl } from '../../insecure';

  interface Props {
    accountId: number;
    onclose: () => void;
  }
  let { accountId, onclose }: Props = $props();

  type Tab = 'general' | 'server' | 'identities' | 'folders' | 'advanced';
  let tab = $state<Tab>('general');
  let settings = $state<AccountSettings | null>(null);
  let loadError = $state<string | null>(null);
  let busy = $state(false);
  let message = $state<{ kind: 'ok' | 'error'; text: string } | null>(null);

  // General
  let displayName = $state('');
  let color = $state('#2563eb');
  let enabled = $state(true);
  let email = $state('');
  let confirmDelete = $state(false);
  let keepData = $state(false);

  // Server
  let incoming = $state<ServerConfig>({ host: '', port: 993, tls: 'tls', auth: 'password', username: '' });
  let outgoing = $state<ServerConfig | null>(null);
  let easUrl = $state('');
  let jmapUrl = $state('');
  let pop3 = $state<Pop3Options>({ leaveOnServer: true, deleteAfterDays: 0, deleteOnLocalDelete: false });
  let sieve = $state<SieveConfig | null>(null);
  let bodyPolicy = $state<BodyPolicy>('recent');
  // Older mails with header only (mail.headersOnlyOlderThanDays of the account).
  let hoMode = $state<'default' | 'all' | 'days'>('default');
  let hoDays = $state(90);
  let globalHoDays = $state<number | null>(null);
  let pollMinutes = $state(0);
  let idleMode = $state<IdleMode>('default');
  let idleFolders = $state<number[]>([]);
  let verify = $state(true);
  let allowInsecure = $state(false);
  let newPassword = $state('');
  let verifyPassword = $state(true);
  let reauth = $state(false);
  let testing = $state(false);
  let test = $state<ConnectionTestResult | null>(null);

  // Advanced
  let index = $state({ content: true, attachments: false, encrypted: false });

  const account = $derived(settings?.account);
  const isMail = $derived(!!account && MAIL_KINDS.includes(account.kind));
  const hasOutgoing = $derived(!!account && (account.kind === 'imap' || account.kind === 'pop3'));
  const oauth = $derived(!!settings?.oauthProvider);
  const oauthProvider = $derived<OAuthProviderId>(settings?.oauthProvider === 'google' ? 'google' : 'microsoft');
  const insecure = $derived(
    incoming.tls === 'none' ||
      (hasOutgoing && outgoing?.tls === 'none') ||
      (account?.kind === 'eas' && isPlainHttpUrl(easUrl)) ||
      (account?.kind === 'jmap' && isPlainHttpUrl(jmapUrl)),
  );
  const folders = $derived(app.folders[accountId] ?? []);

  const tabs = $derived<{ id: Tab; label: string }[]>([
    { id: 'general', label: t('settings.accountEdit.tab.general') },
    ...(settings?.editable ? [{ id: 'server' as Tab, label: t('settings.accountEdit.tab.server') }] : []),
    ...(isMail ? [{ id: 'identities' as Tab, label: t('settings.accountEdit.tab.identities') }, { id: 'folders' as Tab, label: t('settings.accountEdit.tab.folders') }] : []),
    { id: 'advanced', label: t('settings.accountEdit.tab.advanced') },
  ]);

  function msg(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  function fill(s: AccountSettings) {
    settings = s;
    const a = s.account;
    displayName = a.displayName;
    color = a.color || '#2563eb';
    enabled = a.enabled;
    email = s.email;
    incoming = { ...(a.incoming ?? { host: '', port: defaultPort('incoming', a.kind, 'tls'), tls: 'tls', auth: 'password', username: s.email }) };
    outgoing = a.outgoing ? { ...a.outgoing } : a.kind === 'imap' || a.kind === 'pop3' ? { host: '', port: 465, tls: 'tls', auth: 'password', username: s.email } : null;
    easUrl = s.easUrl ?? '';
    jmapUrl = s.jmapUrl ?? '';
    pop3 = { leaveOnServer: true, deleteAfterDays: 0, deleteOnLocalDelete: false, ...s.pop3 };
    sieve = s.sieve ? { ...s.sieve } : null;
    bodyPolicy = s.bodyPolicy;
    const ho = s.headersOnlyOlderThanDays ?? -1;
    hoMode = ho < 0 ? 'default' : ho === 0 ? 'all' : 'days';
    if (ho > 0) hoDays = ho;
    pollMinutes = s.pollMinutes;
    idleMode = s.idleMode;
    idleFolders = [...s.idleFolders];
    index = { ...s.index };
  }

  async function load() {
    try {
      fill(await app.api.getAccountSettings(accountId));
      if (isMail && !app.folders[accountId]) void app.reloadFolders(accountId).catch(() => undefined);
      if (settings?.headersOnlyAvailable) {
        void app.api.getMailStorageSettings().then(
          (g) => (globalHoDays = g.headersOnlyOlderThanDays),
          () => undefined,
        );
      }
    } catch (err) {
      loadError = msg(err);
    }
  }
  $effect(() => {
    void load();
  });

  async function run<T>(f: () => Promise<T>, ok?: string): Promise<T | undefined> {
    busy = true;
    message = null;
    try {
      const v = await f();
      if (ok) message = { kind: 'ok', text: ok };
      return v;
    } catch (err) {
      message = { kind: 'error', text: err instanceof ApiError && err.code === 'connection_failed' ? t('settings.accountEdit.connectionFailed', { error: err.message }) : msg(err) };
      return undefined;
    } finally {
      busy = false;
    }
  }

  async function saveGeneral() {
    const s = await run(() => app.api.updateAccount(accountId, { displayName, color, enabled, email: isMail ? email : undefined }), t('settings.accountEdit.saved'));
    if (s) fill(s);
  }

  function serverUpdate(): AccountUpdate {
    const k = account?.kind;
    const u: AccountUpdate = { incoming: { ...incoming, port: Number(incoming.port) } };
    if (hasOutgoing && outgoing) u.outgoing = { ...outgoing, port: Number(outgoing.port) };
    if (k === 'eas') u.easUrl = easUrl;
    if (k === 'jmap') u.jmapUrl = jmapUrl;
    if (insecure && allowInsecure) u.allowInsecure = true;
    if (k === 'pop3') u.pop3 = { ...pop3, deleteAfterDays: Math.max(0, Number(pop3.deleteAfterDays) || 0) };
    if (k === 'caldav' || k === 'carddav') return u;
    if (settings?.headersOnlyAvailable) {
      // The day count takes precedence over the older body policy, which stays as it is.
      u.headersOnlyOlderThanDays = hoMode === 'default' ? -1 : hoMode === 'all' ? 0 : Math.min(36500, Math.max(1, Math.round(Number(hoDays) || 1)));
    } else {
      u.bodyPolicy = bodyPolicy;
    }
    u.pollMinutes = Math.max(0, Number(pollMinutes) || 0);
    u.idleMode = idleMode;
    u.idleFolders = idleMode === 'custom' ? idleFolders : [];
    if (sieve) u.sieve = { ...sieve, port: Number(sieve.port) || 4190 };
    return u;
  }

  async function saveServer() {
    if (insecure && !allowInsecure) {
      message = { kind: 'error', text: t('onboarding.account.errInsecure') };
      return;
    }
    // Another server, port or user needs its password: the server refuses to send the stored one there.
    const s = await run(() => app.api.updateAccount(accountId, { ...serverUpdate(), verify, password: newPassword || undefined }), t('settings.accountEdit.saved'));
    if (s) {
      fill(s);
      newPassword = '';
    }
  }

  async function runTest() {
    testing = true;
    test = null;
    message = null;
    try {
      test = await app.api.testAccountConnection(accountId, { update: serverUpdate(), password: newPassword || undefined });
    } catch (err) {
      message = { kind: 'error', text: msg(err) };
    } finally {
      testing = false;
    }
  }

  async function savePassword() {
    const s = await run(() => app.api.changeAccountPassword(accountId, newPassword, verifyPassword), t('settings.accountEdit.passwordSaved'));
    if (s) {
      fill(s);
      newPassword = '';
    }
  }

  async function syncNow() {
    await run(() => app.api.syncAccountNow(accountId), t('settings.accountEdit.syncStarted'));
    const s = await app.api.getAccountSettings(accountId).catch(() => null);
    if (s) settings = s;
  }

  async function remove() {
    const ok = await run(() => app.api.deleteAccount(accountId, { keepData }).then(() => true));
    if (!ok) return;
    const accounts = await app.api.listAccounts();
    app.accounts = accounts.sort((a, b) => a.sortOrder - b.sortOrder);
    app.identities = await app.api.listIdentities();
    app.toast(keepData ? t('settings.accountEdit.removedKept', { name: displayName }) : t('settings.accountEdit.removed', { name: displayName }));
    onclose();
  }

  async function saveIndex() {
    const s = await run(() => app.api.updateAccount(accountId, { index }), t('settings.accountEdit.saved'));
    if (s) fill(s);
  }

  async function exportDiagnostics() {
    const d = await run(() => app.api.accountDiagnostics(accountId));
    if (!d) return;
    const blob = new Blob([JSON.stringify(d, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `tuxmail-diagnostics-${accountId}.json`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    message = { kind: 'ok', text: t('settings.accountEdit.diagnosticsDone') };
  }

  function onTls(which: 'incoming' | 'outgoing') {
    const cfg = which === 'incoming' ? incoming : outgoing;
    if (cfg && account) cfg.port = defaultPort(which, account.kind, cfg.tls);
  }

  function toggleIdle(id: number, on: boolean) {
    idleFolders = on ? [...new Set([...idleFolders, id])] : idleFolders.filter((f) => f !== id);
  }

  const states = ['online', 'offline', 'connecting', 'syncing', 'idle', 'error', 'stopped'];
  const stateLabel = (s?: string) => t(`settings.accountEdit.state.${s && states.includes(s) ? s : 'stopped'}` as MessageKey);
  const codeLabel = (c?: string) => (c ? t(`settings.accountEdit.code.${['auth_failed', 'unreachable', 'tls', 'timeout', 'oauth_reauth_required', 'not_configured'].includes(c) ? c : 'error'}` as MessageKey) : '');

  const tlsModes: TLSMode[] = ['tls', 'starttls', 'none'];
  const authMethods: AuthMethod[] = ['password', 'oauth2', 'client_cert'];
  const bodyPolicies: BodyPolicy[] = ['recent', 'all', 'on_demand'];
  const hoModes = ['default', 'all', 'days'] as const;
  const hoDefaultLabel = $derived(
    t('settings.accountEdit.headersOnlyMode.default', {
      value:
        globalHoDays == null || globalHoDays < 0
          ? t('settings.accountEdit.headersOnlyOff')
          : globalHoDays === 0
            ? t('settings.accountEdit.headersOnlyAll')
            : t('settings.accountEdit.headersOnlyDays', { days: globalHoDays }),
    }),
  );
  const idleModes: IdleMode[] = ['default', 'custom', 'off'];
</script>

{#snippet serverFields(cfg: ServerConfig, which: 'incoming' | 'outgoing')}
  <div class="grid">
    <label class="wide"><span class="label">{t('onboarding.account.host')}</span><input class="input" bind:value={cfg.host} required /></label>
    <label><span class="label">{t('onboarding.account.port')}</span><input class="input" type="number" min="1" max="65535" bind:value={cfg.port} required /></label>
    <label>
      <span class="label">{t('onboarding.account.tls')}</span>
      <select class="input" bind:value={cfg.tls} onchange={() => onTls(which)}>
        {#each tlsModes as m (m)}<option value={m}>{t(`settings.tls.${m}`)}</option>{/each}
      </select>
    </label>
    <label>
      <span class="label">{t('onboarding.account.auth')}</span>
      <select class="input" bind:value={cfg.auth}>
        {#each authMethods as a (a)}<option value={a}>{t(`settings.auth.${a}`)}</option>{/each}
      </select>
    </label>
    <label class="wide"><span class="label">{t('onboarding.account.username')}</span><input class="input" bind:value={cfg.username} autocomplete="username" /></label>
  </div>
{/snippet}

<Modal title={t('settings.accountEdit.title', { name: account?.displayName ?? '' })} {onclose} width="720px">
  {#if loadError}
    <p class="error" role="alert">{loadError}</p>
  {:else if !settings || !account}
    <p class="hint"><LoaderCircle size={15} class="spin" /> {t('settings.accountEdit.loading')}</p>
  {:else}
    <div class="tabs" role="tablist" aria-label={t('settings.accountEdit.tabs')}>
      {#each tabs as tb (tb.id)}
        <button role="tab" class:active={tab === tb.id} aria-selected={tab === tb.id} onclick={() => ((tab = tb.id), (message = null))}>{tb.label}</button>
      {/each}
    </div>

    <div class="panel" role="tabpanel">
      {#if tab === 'general'}
        <div class="status" class:err={settings.status?.state === 'error'}>
          <span class="badge {settings.status?.state ?? 'stopped'}">{stateLabel(settings.status?.state)}</span>
          <span class="hint">
            {#if settings.status?.lastSync}{t('settings.accountEdit.lastSync', { when: fmt().dateTime(settings.status.lastSync) })}{:else}{t('settings.accountEdit.neverSynced')}{/if}
          </span>
          <span class="grow"></span>
          <button class="btn" disabled={busy || !enabled} onclick={syncNow}><RefreshCw size={14} /> {t('settings.accountEdit.syncNow')}</button>
        </div>
        {#if settings.status?.error}
          <div class="errbox" role="alert">
            <TriangleAlert size={16} />
            <span>{settings.status.error}</span>
            <button class="btn btn-ghost small" disabled={busy} onclick={syncNow}>{t('settings.accountEdit.retry')}</button>
          </div>
        {/if}
        <form
          class="form"
          onsubmit={(e) => {
            e.preventDefault();
            void saveGeneral();
          }}
        >
          <div class="grid">
            <label class="wide"><span class="label">{t('settings.accountEdit.displayName')}</span><input class="input" bind:value={displayName} required data-autofocus /></label>
            {#if isMail}
              <label class="wide"><span class="label">{t('onboarding.account.email')}</span><input class="input" type="email" bind:value={email} /></label>
            {/if}
            <label><span class="label">{t('settings.accountEdit.color')}</span><input class="input color" type="color" bind:value={color} /></label>
            <label class="check toggle"><input type="checkbox" bind:checked={enabled} /> {t('settings.accountEdit.enabled')}</label>
          </div>
          <p class="hint">{t('settings.accountEdit.kind', { kind: t(`settings.protocol.${account.kind}` as 'settings.protocol.imap') })}</p>
          <div class="row-actions">
            <button type="submit" class="btn btn-primary" disabled={busy}>{t('common.action.save')}</button>
          </div>
        </form>

        <section class="danger" aria-labelledby="acc-remove-h">
          <h3 id="acc-remove-h">{t('settings.accountEdit.remove')}</h3>
          {#if !confirmDelete}
            <p class="hint">{t('settings.accountEdit.removeHint')}</p>
            <button class="btn danger-btn" onclick={() => (confirmDelete = true)}><Trash size={14} /> {t('settings.accountEdit.remove')}</button>
          {:else}
            <p>{t('settings.accountEdit.removeConfirm', { name: account.displayName })}</p>
            {#if isMail}
              <label class="check"><input type="checkbox" bind:checked={keepData} /> {t('settings.accountEdit.keepData')}</label>
            {/if}
            <div class="row-actions">
              <button class="btn btn-ghost" onclick={() => (confirmDelete = false)}>{t('common.action.cancel')}</button>
              <button class="btn danger-btn" disabled={busy} onclick={remove}><Trash size={14} /> {t('settings.accountEdit.removeNow')}</button>
            </div>
          {/if}
        </section>
      {:else if tab === 'server'}
        <form
          class="form"
          onsubmit={(e) => {
            e.preventDefault();
            void saveServer();
          }}
        >
          <fieldset>
            <legend>{t('onboarding.account.incoming')} · {t(`settings.protocol.${account.kind}` as 'settings.protocol.imap')}</legend>
            {#if account.kind === 'eas'}
              <label class="block"><span class="label">{t('onboarding.account.easUrl')}</span><input class="input" type="url" bind:value={easUrl} placeholder="https://mail.example.com/Microsoft-Server-ActiveSync" /></label>
            {/if}
            {#if account.kind === 'jmap'}
              <label class="block"><span class="label">{t('onboarding.account.jmapUrl')}</span><input class="input" type="url" bind:value={jmapUrl} placeholder="https://mail.example.org/.well-known/jmap" /></label>
            {/if}
            {@render serverFields(incoming, 'incoming')}
          </fieldset>
          {#if hasOutgoing && outgoing}
            <fieldset>
              <legend>{t('onboarding.account.outgoing')}</legend>
              {@render serverFields(outgoing, 'outgoing')}
            </fieldset>
          {/if}

          <fieldset>
            <legend>{t('settings.accountEdit.signIn')}</legend>
            {#if oauth}
              {#if reauth}
                <OAuthSignIn provider={oauthProvider} {email} name={displayName} {accountId} accountKind={account.kind} ondone={() => ((reauth = false), (message = { kind: 'ok', text: t('settings.accountEdit.reauthDone') }), void load())} />
              {:else}
                <p class="hint">{t('settings.accountEdit.oauthHint')}</p>
                <button type="button" class="btn" onclick={() => (reauth = true)}><LogIn size={14} /> {t('settings.accountEdit.reauth')}</button>
              {/if}
            {:else}
              <p class="hint">{settings.hasPassword ? t('settings.accountEdit.passwordStored') : t('settings.accountEdit.noPassword')}</p>
              <div class="pw">
                <label class="grow"><span class="label">{t('settings.accountEdit.newPassword')}</span><input class="input" type="password" bind:value={newPassword} autocomplete="new-password" /></label>
                <button type="button" class="btn" disabled={busy || !newPassword} onclick={savePassword}><KeyRound size={14} /> {t('settings.accountEdit.changePassword')}</button>
              </div>
              <label class="check"><input type="checkbox" bind:checked={verifyPassword} /> {t('settings.accountEdit.verifyPassword')}</label>
            {/if}
          </fieldset>

          <details class="advanced">
            <summary>{t('settings.accountEdit.advanced')}</summary>
            {#if account.kind === 'pop3'}
              <fieldset>
                <legend>{t('onboarding.account.pop3.title')}</legend>
                <label class="check"><input type="checkbox" bind:checked={pop3.leaveOnServer} /> {t('onboarding.account.pop3.leave')}</label>
                {#if pop3.leaveOnServer}
                  <label class="check">
                    {t('onboarding.account.pop3.deleteAfter')}
                    <input class="input days" type="number" min="0" max="3650" bind:value={pop3.deleteAfterDays} aria-label={t('onboarding.account.pop3.days')} />
                    {t('onboarding.account.pop3.days')}
                  </label>
                  <label class="check"><input type="checkbox" bind:checked={pop3.deleteOnLocalDelete} /> {t('onboarding.account.pop3.deleteLocal')}</label>
                {/if}
              </fieldset>
            {/if}
            {#if account.kind !== 'caldav' && account.kind !== 'carddav'}
              {#if settings?.headersOnlyAvailable}
                <div class="grid">
                  <label class="wide">
                    <span class="label">{t('settings.accountEdit.headersOnly')}</span>
                    <select class="input" bind:value={hoMode} aria-describedby="acc-ho-hint" data-testid="account-headers-only">
                      {#each hoModes as m (m)}<option value={m}>{m === 'default' ? hoDefaultLabel : t(`settings.accountEdit.headersOnlyMode.${m}`)}</option>{/each}
                    </select>
                  </label>
                  {#if hoMode === 'days'}
                    <label>
                      <span class="label">{t('settings.storage.daysLabel')}</span>
                      <input class="input" type="number" min="1" max="36500" step="1" bind:value={hoDays} data-testid="account-headers-only-days" />
                    </label>
                  {/if}
                </div>
                <p class="hint" id="acc-ho-hint">{t('settings.accountEdit.headersOnlyHint')}</p>
              {/if}
              <div class="grid">
                {#if !settings?.headersOnlyAvailable}
                  <label>
                    <span class="label">{t('settings.accountEdit.bodyPolicy')}</span>
                    <select class="input" bind:value={bodyPolicy}>
                      {#each bodyPolicies as p (p)}<option value={p}>{t(`settings.accountEdit.body.${p}`)}</option>{/each}
                    </select>
                  </label>
                {/if}
                <label>
                  <span class="label">{t('settings.accountEdit.pollMinutes')}</span>
                  <input class="input" type="number" min="0" max="1440" bind:value={pollMinutes} />
                </label>
                <label class="wide">
                  <span class="label">{t('settings.accountEdit.idle')}</span>
                  <select class="input" bind:value={idleMode}>
                    {#each idleModes as m (m)}<option value={m}>{t(`settings.accountEdit.idleMode.${m}`)}</option>{/each}
                  </select>
                </label>
              </div>
              <p class="hint">{t('settings.accountEdit.pollHint')}</p>
              {#if idleMode === 'custom'}
                <div class="idle-list" role="group" aria-label={t('settings.accountEdit.idle')}>
                  {#each folders as f (f.id)}
                    <label class="check"><input type="checkbox" checked={idleFolders.includes(f.id)} onchange={(e) => toggleIdle(f.id, e.currentTarget.checked)} /> {f.name}</label>
                  {:else}
                    <p class="hint">{t('settings.accountEdit.noFolders')}</p>
                  {/each}
                </div>
              {/if}
            {/if}
            {#if sieve}
              <fieldset>
                <legend>{t('settings.sieve.title')}</legend>
                <label class="check"><input type="checkbox" checked={!sieve.disabled} onchange={(e) => sieve && (sieve.disabled = !e.currentTarget.checked)} /> {t('settings.accountEdit.sieveEnabled')}</label>
                {#if !sieve.disabled}
                  <div class="grid">
                    <label class="wide"><span class="label">{t('onboarding.account.host')}</span><input class="input" bind:value={sieve.host} /></label>
                    <label><span class="label">{t('onboarding.account.port')}</span><input class="input" type="number" min="1" max="65535" bind:value={sieve.port} /></label>
                    <label>
                      <span class="label">{t('onboarding.account.tls')}</span>
                      <select class="input" bind:value={sieve.tls}>
                        {#each ['starttls', 'tls', 'none'] as const as m (m)}<option value={m}>{t(`settings.tls.${m}`)}</option>{/each}
                      </select>
                    </label>
                  </div>
                  <label class="check"><input type="checkbox" bind:checked={sieve.separate} /> {t('settings.sieve.separate')}</label>
                {/if}
                <button
                  type="button"
                  class="btn btn-ghost small"
                  onclick={() => {
                    app.openSettings('accounts', 'sieve');
                    onclose();
                  }}>{t('settings.accountEdit.sieveScripts')}</button
                >
              </fieldset>
            {/if}
          </details>

          {#if insecure}
            <label class="warn">
              <TriangleAlert size={16} />
              <span>{t('onboarding.account.insecureWarning')}</span>
              <input type="checkbox" bind:checked={allowInsecure} aria-label={t('onboarding.account.insecureConfirm')} />
            </label>
          {/if}

          {#if test}
            <ul class="tests" aria-live="polite">
              {#each test.steps as s (s.kind)}
                <li class:ok={s.ok}>
                  {#if s.ok}<CircleCheck size={15} />{:else}<CircleX size={15} />{/if}
                  <strong>{t(`settings.accountEdit.test.${s.kind}` as MessageKey)}</strong>
                  <span class="target">{s.target}</span>
                  {#if s.ok}<span class="hint">{t('settings.accountEdit.testOk', { ms: s.millis })}</span>{:else}<span class="fail">{codeLabel(s.code)}{#if s.error}: {s.error}{/if}</span>{/if}
                </li>
              {/each}
            </ul>
          {/if}

          <label class="check"><input type="checkbox" bind:checked={verify} /> {t('settings.accountEdit.verify')}</label>
          <div class="row-actions">
            <button type="button" class="btn" disabled={testing || busy} onclick={runTest}>
              {#if testing}<LoaderCircle size={14} class="spin" />{:else}<PlugZap size={14} />{/if}
              {t('settings.accountEdit.testConnection')}
            </button>
            <button type="submit" class="btn btn-primary" disabled={busy}>
              {#if busy}<LoaderCircle size={14} class="spin" />{/if}
              {t('common.action.save')}
            </button>
          </div>
        </form>
      {:else if tab === 'identities'}
        <AccountIdentities {accountId} accountKind={account.kind} />
      {:else if tab === 'folders'}
        <FolderSettings fixedAccountId={accountId} />
      {:else if tab === 'advanced'}
        <fieldset>
          <legend>{t('settings.accountEdit.index')}</legend>
          <p class="hint">{t('settings.accountEdit.indexHint')}</p>
          <label class="check"><input type="checkbox" bind:checked={index.content} /> {t('settings.accountEdit.indexContent')}</label>
          <label class="check"><input type="checkbox" bind:checked={index.attachments} disabled={!index.content} /> {t('settings.accountEdit.indexAttachments')}</label>
          <label class="check"><input type="checkbox" bind:checked={index.encrypted} disabled={!index.content} /> {t('settings.accountEdit.indexEncrypted')}</label>
          <div class="row-actions">
            <button class="btn btn-primary" disabled={busy} onclick={saveIndex}>{t('common.action.save')}</button>
          </div>
        </fieldset>
        <fieldset>
          <legend>{t('settings.accountEdit.diagnostics')}</legend>
          <p class="hint">{t('settings.accountEdit.diagnosticsHint')}</p>
          <button class="btn" disabled={busy} onclick={exportDiagnostics}><Download size={14} /> {t('settings.accountEdit.diagnosticsExport')}</button>
        </fieldset>
      {/if}

      {#if message}
        <p class={message.kind === 'ok' ? 'okmsg' : 'error'} role={message.kind === 'ok' ? 'status' : 'alert'}>{message.text}</p>
      {/if}
    </div>
  {/if}
</Modal>

<style>
  .tabs {
    display: flex;
    gap: 2px;
    border-bottom: 1px solid var(--tm-line);
    margin: -4px 0 14px;
    overflow-x: auto;
  }
  .tabs button {
    background: none;
    border: none;
    border-bottom: 2px solid transparent;
    padding: 8px 12px;
    font: inherit;
    font-size: 13px;
    color: var(--tm-muted);
    cursor: pointer;
    white-space: nowrap;
  }
  .tabs button.active {
    color: var(--tm-accent-text);
    border-bottom-color: var(--tm-accent);
    font-weight: 600;
  }
  .panel {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .grid .wide {
    grid-column: span 2;
  }
  .block {
    display: block;
    margin-bottom: 10px;
  }
  .block .input {
    width: 100%;
  }
  fieldset {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 10px 12px 12px;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  legend {
    font-size: 12.5px;
    font-weight: 600;
    padding: 0 4px;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .toggle {
    align-self: end;
    padding-bottom: 6px;
  }
  .color {
    height: 34px;
    padding: 2px 4px;
    width: 64px;
  }
  .days {
    width: 80px;
  }
  .hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .okmsg {
    margin: 0;
    color: var(--tm-success);
    font-size: 13px;
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
  .status {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 10px;
    padding: 10px 12px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
  }
  .grow {
    flex: 1;
  }
  .badge {
    flex: none;
    white-space: nowrap;
    font-size: 11.5px;
    padding: 2px 9px;
    border-radius: 99px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
    font-weight: 600;
  }
  .badge.online,
  .badge.syncing {
    background: color-mix(in srgb, var(--tm-success) 16%, var(--tm-surface));
    color: var(--tm-success);
  }
  .badge.error {
    background: color-mix(in srgb, var(--tm-danger) 14%, var(--tm-surface));
    color: var(--tm-danger);
  }
  .errbox {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--tm-danger) 10%, var(--tm-surface));
    color: var(--tm-danger);
    font-size: 13px;
  }
  .errbox span {
    flex: 1;
  }
  .btn.small {
    height: 26px;
    font-size: 12px;
    padding: 0 8px;
    align-self: flex-start;
  }
  .danger {
    margin-top: 6px;
    padding-top: 12px;
    border-top: 1px solid var(--tm-line);
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .danger h3 {
    margin: 0;
    font-size: 13px;
    font-weight: 650;
  }
  .danger p {
    margin: 0;
    font-size: 13px;
  }
  .danger-btn {
    color: var(--tm-danger);
    border-color: color-mix(in srgb, var(--tm-danger) 40%, var(--tm-line));
    align-self: flex-start;
  }
  .pw {
    display: flex;
    align-items: flex-end;
    gap: 8px;
  }
  .pw .input {
    width: 100%;
  }
  .advanced summary {
    cursor: pointer;
    font-size: 13px;
    font-weight: 600;
    margin-bottom: 8px;
  }
  .advanced[open] {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .idle-list {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
    gap: 4px 12px;
  }
  .warn {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--tm-warn) 14%, var(--tm-surface));
    font-size: 13px;
  }
  .warn span {
    flex: 1;
  }
  .tests {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 13px;
  }
  .tests li {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    color: var(--tm-danger);
  }
  .tests li.ok {
    color: var(--tm-success);
  }
  .tests strong {
    color: var(--tm-fg);
  }
  .target {
    color: var(--tm-muted);
    font-family: var(--tm-mono, monospace);
    font-size: 12px;
  }
  .fail {
    flex-basis: 100%;
    padding-left: 23px;
    font-size: 12.5px;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .grid {
      grid-template-columns: 1fr;
    }
    .grid .wide {
      grid-column: auto;
    }
    .pw {
      flex-direction: column;
      align-items: stretch;
    }
  }
</style>
