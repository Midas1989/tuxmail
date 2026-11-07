<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Account setup skeleton (KONZEPT 7.5 / 16.2 onboarding):
  e-mail + password -> discovered settings -> manual server form.
-->
<script lang="ts" module>
  import type { TLSMode } from '../../types';

  export function defaultPort(kind: 'incoming' | 'outgoing', protocol: string, tls: TLSMode): number {
    if (kind === 'outgoing') return tls === 'tls' ? 465 : 587;
    if (protocol === 'pop3') return tls === 'tls' ? 995 : 110;
    if (protocol === 'eas' || protocol === 'graph' || protocol === 'ews' || protocol === 'jmap') return 443;
    return tls === 'tls' ? 993 : 143;
  }
</script>

<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import Server from '@lucide/svelte/icons/server';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import { app } from '../../state';
  import { t, type MessageKey } from '../../i18n/index.svelte';
  import type { AccountKind, AuthMethod, DiscoveryCandidate, DiscoveryCheck, DiscoveryResult, Pop3Options, ServerConfig } from '../../types';
  import CircleX from '@lucide/svelte/icons/circle-x';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import Modal from '../common/Modal.svelte';
  import logoUrl from '$branding/tuxmail-logo.png';
  import CloudConnectForm, { CLOUD_PROVIDERS } from '../cloud/CloudConnectForm.svelte';
  import ProviderIcon from '../cloud/ProviderIcon.svelte';
  import type { CloudAccountProvider } from '../../types';
  import { isPlainHttpUrl } from '../../insecure';
  import OAuthSignIn from './OAuthSignIn.svelte';

  interface Props {
    onclose: () => void;
  }
  let { onclose }: Props = $props();

  /** Cloud accounts (KONZEPT 12) are added through their own login flows. */
  let cloudProvider = $state<CloudAccountProvider | null>(null);

  type Step = 'credentials' | 'discovering' | 'discovered' | 'manual' | 'saving' | 'done';
  let step = $state<Step>('credentials');
  let name = $state('');
  let email = $state('');
  let password = $state('');
  let error = $state<string | null>(null);
  let discovery = $state<DiscoveryResult | null>(null);

  let protocol = $state<AccountKind>('imap');
  let incoming = $state<ServerConfig>({ host: '', port: 993, tls: 'tls', auth: 'password', username: '' });
  let outgoing = $state<ServerConfig>({ host: '', port: 465, tls: 'tls', auth: 'password', username: '' });
  let allowInsecure = $state(false);
  let easUrl = $state('');
  let jmapUrl = $state('');
  let pop3 = $state<Pop3Options>({ leaveOnServer: true, deleteAfterDays: 0, deleteOnLocalDelete: false });
  let usePassword = $state(false); // OAuth/Cloud accounts: skip the sign-in step
  /** Index of the chosen candidate in discovery.candidates. */
  let chosen = $state(0);
  let othersOpen = $state(false);

  function useCandidate(i: number) {
    const c = discovery?.candidates?.[i];
    if (!c) return;
    chosen = i;
    protocol = c.kind;
    if (c.incoming) incoming = { ...c.incoming };
    if (c.outgoing) outgoing = { ...c.outgoing };
    easUrl = c.easUrl ?? '';
  }

  function checkText(c: DiscoveryCheck, srv?: ServerConfig): string {
    if (!srv) return t('onboarding.account.check.unknown');
    if (!c.reachable) return t('onboarding.account.check.unreachable', { reason: reasonText(c.error) });
    if (c.login === 'ok') return t('onboarding.account.check.loginOk');
    if (c.login === 'failed') return t('onboarding.account.check.loginFailed');
    return t('onboarding.account.check.reachable');
  }
  /** The core sends a reason code, never a raw error (DNS answers, foreign host names). */
  function reasonText(code?: string): string {
    const known = ['dns', 'timeout', 'refused', 'tls', 'auth', 'other'];
    return t(`onboarding.account.check.reason.${code && known.includes(code) ? code : 'other'}` as MessageKey);
  }
  function candOk(c: DiscoveryCandidate): boolean {
    return c.kind === 'eas' || (c.in.reachable && c.in.login !== 'failed' && (!c.outgoing || (c.out.reachable && c.out.login !== 'failed')));
  }
  function srvLine(kind: string, s?: ServerConfig): string {
    return s ? `${kind.toUpperCase()} · ${s.host}:${s.port} · ${t(`settings.tls.${s.tls}`)}` : '';
  }

  const needsOutgoing = $derived(protocol === 'imap' || protocol === 'pop3');
  const insecure = $derived(
    incoming.tls === 'none' ||
      (needsOutgoing && outgoing.tls === 'none') ||
      (protocol === 'eas' && isPlainHttpUrl(easUrl)) ||
      (protocol === 'jmap' && isPlainHttpUrl(jmapUrl)),
  );
  const emailOk = $derived(/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(email));

  const steps: { id: Step[]; label: string }[] = $derived([
    { id: ['credentials', 'discovering'], label: t('onboarding.account.step1') },
    { id: ['discovered'], label: t('onboarding.account.step2') },
    { id: ['manual', 'saving'], label: t('onboarding.account.step3') },
  ]);

  async function discover() {
    if (!emailOk) {
      error = t('onboarding.account.errEmail');
      return;
    }
    error = null;
    step = 'discovering';
    try {
      discovery = await app.api.discoverAccount(email, password);
      if (discovery.incoming) incoming = { ...discovery.incoming };
      if (discovery.outgoing) outgoing = { ...discovery.outgoing };
      protocol = discovery.kind;
      easUrl = discovery.easUrl ?? '';
      chosen = 0;
      othersOpen = false;
      step = discovery.source === 'none' ? 'manual' : 'discovered';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      step = 'credentials';
    }
  }

  function manual() {
    if (!incoming.username) incoming.username = email;
    if (!outgoing.username) outgoing.username = email;
    const domain = email.split('@')[1] ?? '';
    if (!incoming.host && domain) incoming.host = `imap.${domain}`;
    if (!outgoing.host && domain) outgoing.host = `smtp.${domain}`;
    step = 'manual';
  }

  function onTls(which: 'incoming' | 'outgoing') {
    const cfg = which === 'incoming' ? incoming : outgoing;
    cfg.port = defaultPort(which, protocol, cfg.tls);
  }

  async function save() {
    if (insecure && !allowInsecure) {
      error = t('onboarding.account.errInsecure');
      return;
    }
    error = null;
    step = 'saving';
    try {
      await app.api.createAccount({
        kind: protocol,
        displayName: email.split('@')[1] ?? email,
        email,
        name,
        password,
        incoming,
        outgoing: needsOutgoing ? outgoing : undefined,
        easUrl: protocol === 'eas' && easUrl ? easUrl : undefined,
        jmapUrl: protocol === 'jmap' && jmapUrl ? jmapUrl : undefined,
        allowInsecure: insecure && allowInsecure ? true : undefined,
        pop3: protocol === 'pop3' ? { ...pop3, deleteAfterDays: Math.max(0, Number(pop3.deleteAfterDays) || 0) } : undefined,
      });
      step = 'done';
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      step = 'manual';
    }
  }

  const protocols: AccountKind[] = ['imap', 'pop3', 'jmap', 'eas', 'graph', 'ews', 'gmail'];
  const tlsModes: TLSMode[] = ['tls', 'starttls', 'none'];
  const authMethods: AuthMethod[] = ['password', 'oauth2', 'client_cert'];
</script>

{#snippet insecureWarning()}
  {#if insecure}
    <label class="warn">
      <TriangleAlert size={16} />
      <span>{t('onboarding.account.insecureWarning')}</span>
      <input type="checkbox" bind:checked={allowInsecure} aria-label={t('onboarding.account.insecureConfirm')} />
    </label>
  {/if}
{/snippet}

<Modal title={cloudProvider ? t('cloud.connect.title') : t('onboarding.account.title')} {onclose} width="600px">
  {#if cloudProvider}
    <CloudConnectForm provider={cloudProvider} ondone={onclose} oncancel={() => (cloudProvider = null)} />
  {:else}
  <ol class="steps" aria-label={t('onboarding.account.progress')}>
    {#each steps as s, i (i)}
      <li class:current={s.id.includes(step)}>{i + 1}. {s.label}</li>
    {/each}
  </ol>

  {#if step === 'credentials' || step === 'discovering'}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void discover();
      }}
    >
      <img class="brand" src={logoUrl} alt="TuxMail" width="120" height="120" />
      <p class="lead">{t('onboarding.account.lead')}</p>
      <label><span class="label">{t('onboarding.account.name')}</span><input class="input" bind:value={name} autocomplete="name" data-autofocus /></label>
      <label><span class="label">{t('onboarding.account.email')}</span><input class="input" type="email" bind:value={email} autocomplete="email" required /></label>
      <label><span class="label">{t('onboarding.account.password')}</span><input class="input" type="password" bind:value={password} autocomplete="current-password" /></label>
      <p class="hint">{t('onboarding.account.passwordHint')}</p>
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button type="button" class="btn btn-ghost" onclick={manual}>{t('onboarding.account.manual')}</button>
        <button type="submit" class="btn btn-primary" disabled={step === 'discovering' || !email}>
          {#if step === 'discovering'}<LoaderCircle size={15} class="spin" /> {t('onboarding.account.discovering')}{:else}{t('onboarding.account.next')}{/if}
        </button>
      </div>
      <div class="cloud-alt">
        <span class="hint">{t('cloud.connect.wizardTitle')}</span>
        <div class="cloud-btns">
          {#each CLOUD_PROVIDERS as p (p)}
            <button type="button" class="btn btn-ghost" onclick={() => (cloudProvider = p)}><ProviderIcon provider={p} /> {t(`cloud.provider.${p}`)}</button>
          {/each}
        </div>
      </div>
    </form>
  {:else if step === 'discovered' && discovery && discovery.oauthProvider && !usePassword}
    <!-- OAuth/Cloud accounts -->
    <OAuthSignIn
      provider={discovery.oauthProvider}
      {email}
      {name}
      ondone={() => (step = 'done')}
      onpassword={() => (usePassword = true)}
      onsettings={() => {
        app.openSettings('security', 'signin');
        onclose();
      }}
    />
  {:else if step === 'discovered' && discovery}
    <div class="found">
      {#if discovery.candidates?.length && !candOk(discovery.candidates[chosen])}
        <p class="warn-line"><TriangleAlert size={18} /> {t('onboarding.account.check.noneWorking')}</p>
      {:else}
        <p class="ok"><CircleCheck size={18} /> {t('onboarding.account.found', { provider: discovery.displayName || email.split('@')[1] || '' })}{discovery.verified ? ' · ' + t('onboarding.account.check.verified') : ''}</p>
      {/if}
      <dl>
        <dt>{t('onboarding.account.incoming')}</dt>
        <dd><Server size={14} /> {discovery.kind.toUpperCase()} · {incoming.host}:{incoming.port} · {t(`settings.tls.${incoming.tls}`)}</dd>
        {#if discovery.outgoing}
          <dt>{t('onboarding.account.outgoing')}</dt>
          <dd><Server size={14} /> SMTP · {outgoing.host}:{outgoing.port} · {t(`settings.tls.${outgoing.tls}`)}</dd>
        {/if}
        <dt>{t('onboarding.account.username')}</dt>
        <dd>{incoming.username}</dd>
      </dl>
      {#if discovery.candidates?.length}
        {@const cur = discovery.candidates[chosen]}
        <button class="others-toggle" aria-expanded={othersOpen} onclick={() => (othersOpen = !othersOpen)} data-testid="wizard-report">
          <ChevronRight size={14} class={othersOpen ? 'rot' : ''} /> {t('onboarding.account.report')}
        </button>
        {#if othersOpen}
          <div class="report" data-testid="wizard-report-body">
            <ul class="checks">
              <li>{t('onboarding.account.reportSource')}: {t(`onboarding.account.source.${cur.source}` as 'onboarding.account.source.autoconfig')}</li>
              {#if cur.kind !== 'eas'}
                <li class:bad={!cur.in.reachable || cur.in.login === 'failed'}>{t('onboarding.account.incoming')}: {checkText(cur.in, cur.incoming)}</li>
                {#if cur.outgoing}<li class:bad={!cur.out.reachable || cur.out.login === 'failed'}>{t('onboarding.account.outgoing')}: {checkText(cur.out, cur.outgoing)}</li>{/if}
                <li>{t('onboarding.account.reportSecurity')}: {t(`settings.tls.${incoming.tls}`)}{needsOutgoing ? ' / ' + t(`settings.tls.${outgoing.tls}`) : ''}</li>
              {/if}
            </ul>
            {#if discovery.candidates.length > 1}
              <p class="alt-h">{t('onboarding.account.alternatives')}</p>
              <ul class="cands">
                {#each discovery.candidates as c, i (i)}
                  {#if i !== chosen}
                    <li>
                      <span class="ci">{#if candOk(c)}<CircleCheck size={15} />{:else}<CircleX size={15} />{/if}</span>
                      <span class="cb">
                        <span class="cl">{c.kind === 'eas' ? 'ActiveSync' : srvLine(c.kind, c.incoming)}</span>
                        {#if c.outgoing}<span class="cl">{srvLine('smtp', c.outgoing)}</span>{/if}
                        {#if c.kind !== 'eas'}<span class="cs">{checkText(c.in, c.incoming)}</span>{/if}
                      </span>
                      <button class="btn btn-ghost" onclick={() => useCandidate(i)}>{t('onboarding.account.useThis')}</button>
                    </li>
                  {/if}
                {/each}
              </ul>
            {/if}
          </div>
        {/if}
      {/if}
      {#if discovery.oauthAvailable}<p class="hint">{t('onboarding.account.oauthHint')}</p>{/if}
      {@render insecureWarning()}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button class="btn btn-ghost" onclick={() => (step = 'manual')}>{t('onboarding.account.editManually')}</button>
        <button class="btn btn-primary" onclick={save} data-autofocus>{t('onboarding.account.addAccount')}</button>
      </div>
    </div>
  {:else if step === 'manual' || step === 'saving'}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <label>
        <span class="label">{t('onboarding.account.protocol')}</span>
        <select class="input" bind:value={protocol} onchange={() => onTls('incoming')}>
          {#each protocols as p (p)}<option value={p}>{t(`settings.protocol.${p}`)}</option>{/each}
        </select>
      </label>
      <fieldset>
        <legend>{t('onboarding.account.incoming')}</legend>
        <div class="grid">
          {#if protocol === 'jmap'}
            <label class="wide"><span class="label">{t('onboarding.account.jmapUrl')}</span><input class="input" type="url" bind:value={jmapUrl} placeholder="https://mail.example.org/.well-known/jmap" /></label>
          {/if}
          {#if protocol === 'eas'}
            <label class="wide"><span class="label">{t('onboarding.account.easUrl')}</span><input class="input" type="url" bind:value={easUrl} placeholder="https://mail.example.com/Microsoft-Server-ActiveSync" /></label>
          {/if}
          <label class="wide"><span class="label">{t('onboarding.account.host')}</span><input class="input" bind:value={incoming.host} required /></label>
          <label><span class="label">{t('onboarding.account.port')}</span><input class="input" type="number" min="1" max="65535" bind:value={incoming.port} required /></label>
          <label>
            <span class="label">{t('onboarding.account.tls')}</span>
            <select class="input" bind:value={incoming.tls} onchange={() => onTls('incoming')}>
              {#each tlsModes as m (m)}<option value={m}>{t(`settings.tls.${m}`)}</option>{/each}
            </select>
          </label>
          <label>
            <span class="label">{t('onboarding.account.auth')}</span>
            <select class="input" bind:value={incoming.auth}>
              {#each authMethods as a (a)}<option value={a}>{t(`settings.auth.${a}`)}</option>{/each}
            </select>
          </label>
          <label class="wide"><span class="label">{t('onboarding.account.username')}</span><input class="input" bind:value={incoming.username} /></label>
        </div>
      </fieldset>
      {#if protocol === 'pop3'}
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
          <p class="hint">{t('onboarding.account.pop3.hint')}</p>
        </fieldset>
      {/if}
      {#if needsOutgoing}
        <fieldset>
          <legend>{t('onboarding.account.outgoing')}</legend>
          <div class="grid">
            <label class="wide"><span class="label">{t('onboarding.account.host')}</span><input class="input" bind:value={outgoing.host} required /></label>
            <label><span class="label">{t('onboarding.account.port')}</span><input class="input" type="number" min="1" max="65535" bind:value={outgoing.port} required /></label>
            <label>
              <span class="label">{t('onboarding.account.tls')}</span>
              <select class="input" bind:value={outgoing.tls} onchange={() => onTls('outgoing')}>
                {#each tlsModes as m (m)}<option value={m}>{t(`settings.tls.${m}`)}</option>{/each}
              </select>
            </label>
            <label>
              <span class="label">{t('onboarding.account.auth')}</span>
              <select class="input" bind:value={outgoing.auth}>
                {#each authMethods as a (a)}<option value={a}>{t(`settings.auth.${a}`)}</option>{/each}
              </select>
            </label>
            <label class="wide"><span class="label">{t('onboarding.account.username')}</span><input class="input" bind:value={outgoing.username} /></label>
          </div>
        </fieldset>
      {/if}
      {@render insecureWarning()}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button type="button" class="btn btn-ghost" onclick={() => (step = 'credentials')}>{t('common.action.back')}</button>
        <button type="submit" class="btn btn-primary" disabled={step === 'saving'}>
          {#if step === 'saving'}<LoaderCircle size={15} class="spin" />{/if}
          {t('onboarding.account.addAccount')}
        </button>
      </div>
    </form>
  {:else if step === 'done'}
    <div class="done">
      <CircleCheck size={40} />
      <p>{t('onboarding.account.done', { email })}</p>
      <button class="btn btn-primary" onclick={onclose} data-autofocus>{t('common.action.close')}</button>
    </div>
  {/if}
  {/if}
</Modal>

<style>
  .warn-line {
    display: flex;
    gap: 8px;
    align-items: center;
    color: var(--tm-warn);
    font-weight: 600;
  }
  .checks {
    list-style: none;
    padding: 0;
    margin: 6px 0;
    font-size: 12.5px;
    color: var(--tm-ok, #15803d);
  }
  .checks li.bad {
    color: var(--tm-danger);
  }
  .others-toggle {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    border: none;
    background: none;
    color: var(--tm-accent);
    font: inherit;
    font-size: 13px;
    padding: 4px 0;
    cursor: pointer;
  }
  .others-toggle :global(.rot) {
    transform: rotate(90deg);
  }
  .alt-h {
    margin: 10px 0 4px;
    font-size: 12.5px;
    font-weight: 600;
  }
  .cands {
    list-style: none;
    margin: 4px 0 8px;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 260px;
    overflow: auto;
  }
  .cands li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    font-size: 12.5px;
  }
  .ci {
    display: inline-flex;
    color: var(--tm-muted);
  }
  .cb {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .cl {
    overflow-wrap: anywhere;
  }
  .cs {
    color: var(--tm-muted);
    overflow-wrap: anywhere;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 6px 0;
    font-size: 13px;
  }
  .days {
    width: 80px;
  }
  .brand {
    display: block;
    width: 120px;
    height: 120px;
    margin: 0 auto 4px;
    border-radius: 18px;
  }
  .steps {
    display: flex;
    gap: 16px;
    list-style: none;
    margin: 0 0 16px;
    padding: 0;
    font-size: 12.5px;
    color: var(--tm-faint);
    flex-wrap: wrap;
  }
  .steps li.current {
    color: var(--tm-accent-text);
    font-weight: 600;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .lead {
    margin: 0;
    color: var(--tm-muted);
    font-size: 13.5px;
  }
  .hint {
    margin: -4px 0 0;
    font-size: 12px;
    color: var(--tm-faint);
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .cloud-alt {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding-top: 12px;
    border-top: 1px solid var(--tm-line);
  }
  .cloud-btns {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }
  fieldset {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 10px 12px 12px;
    margin: 0;
  }
  legend {
    font-size: 12.5px;
    font-weight: 600;
    padding: 0 4px;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
  .grid .wide {
    grid-column: span 2;
  }
  .found dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 14px;
    margin: 12px 0;
    font-size: 13.5px;
  }
  .found dt {
    color: var(--tm-muted);
  }
  .found dd {
    margin: 0;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .ok {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    color: var(--tm-success);
    font-weight: 600;
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
  .done {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 16px;
    color: var(--tm-success);
  }
  .done p {
    color: var(--tm-fg);
    margin: 0;
  }
  :global(.spin) {
    animation: spin 0.9s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
