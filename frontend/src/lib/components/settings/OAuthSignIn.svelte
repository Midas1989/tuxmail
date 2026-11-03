<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  OAuth step of the account wizard (KONZEPT 7.3–7.5): choose the access
  method, "Mit Microsoft/Google anmelden", wait for the browser sign-in
  (desktop loopback, web redirect or device code) and report the result.
-->
<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import LogIn from '@lucide/svelte/icons/log-in';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { ApiError } from '../../api';
  import type { AccountKind, OAuthFlowStatus, OAuthProviderId } from '../../types';

  interface Props {
    provider: OAuthProviderId;
    email: string;
    name: string;
    ondone: (accountId: number) => void;
    /** Continue with password/app password instead. */
    onpassword?: () => void;
    /** Leave the wizard to open the settings. */
    onsettings?: () => void;
  }
  let { provider, email, name, ondone, onpassword, onsettings }: Props = $props();

  const kindsFor: Record<OAuthProviderId, AccountKind[]> = {
    microsoft: ['graph', 'imap', 'eas', 'ews'],
    google: ['gmail', 'imap'],
  };
  const displayName = $derived(provider === 'microsoft' ? 'Microsoft' : 'Google');
  let chosen = $state<AccountKind | null>(null);
  const kind = $derived<AccountKind>(chosen ?? kindsFor[provider][0]);
  let flow = $state<OAuthFlowStatus | null>(null);
  let errorCode = $state<string | null>(null);
  let errorText = $state('');
  let timer: ReturnType<typeof setInterval> | null = null;

  const isWeb = $derived(app.api.kind === 'http');
  const deviceAvailable = $derived(provider === 'microsoft');

  function stop() {
    if (timer) clearInterval(timer);
    timer = null;
  }
  $effect(() => stop);

  function fail(code: string, text: string) {
    errorCode = code;
    errorText = text;
    flow = null;
    stop();
  }

  async function start(mode?: 'device') {
    errorCode = null;
    try {
      flow = await app.api.startOAuth({ provider, email, kind, name, displayName: email, mode });
      if (flow.mode === 'redirect' && flow.authUrl) window.open(flow.authUrl, '_blank', 'noopener');
      timer = setInterval(poll, 1000);
    } catch (err) {
      fail(err instanceof ApiError ? err.code : 'error', err instanceof Error ? err.message : String(err));
    }
  }

  async function poll() {
    if (!flow) return;
    try {
      const s = await app.api.getOAuthFlow(flow.id);
      flow = s;
      if (s.status === 'done' && s.accountId) {
        stop();
        const [accounts, identities] = await Promise.all([app.api.listAccounts(), app.api.listIdentities()]);
        app.accounts = accounts.sort((a, b) => a.sortOrder - b.sortOrder);
        app.identities = identities;
        ondone(s.accountId);
      } else if (s.status === 'error' || s.status === 'canceled') {
        fail(s.errorCode ?? 'error', s.error ?? '');
      }
    } catch (err) {
      fail('error', err instanceof Error ? err.message : String(err));
    }
  }

  async function cancel() {
    if (flow) await app.api.cancelOAuth(flow.id).catch(() => undefined);
    flow = null;
    stop();
  }

  const errorMessage = $derived.by(() => {
    switch (errorCode) {
      case null:
        return '';
      case 'oauth_not_configured':
        return t('cloud.signIn.notConfigured', { provider: displayName });
      case 'oauth_consent_required':
        return t('cloud.signIn.consent');
      case 'oauth_denied':
        return t('cloud.signIn.denied');
      case 'oauth_reauth_required':
        return t('cloud.signIn.expired');
      default:
        return t('cloud.signIn.failed', { error: errorText });
    }
  });
</script>

<div class="oauth">
  <p class="lead">{t('cloud.signIn.lead', { provider: displayName })}</p>
  {#if !flow}
    <label>
      <span class="label">{t('cloud.signIn.method')}</span>
      <select class="input" value={kind} onchange={(e) => (chosen = e.currentTarget.value as AccountKind)}>
        {#each kindsFor[provider] as k (k)}<option value={k}>{t(`cloud.signIn.kind.${k}` as 'cloud.signIn.kind.imap')}</option>{/each}
      </select>
    </label>
    {#if errorMessage}
      <p class="error" role="alert">{errorMessage}</p>
      {#if errorCode === 'oauth_not_configured' && onsettings}
        <button type="button" class="btn btn-ghost" onclick={onsettings}>{t('cloud.signIn.openSettings')}</button>
      {/if}
    {/if}
    <div class="row-actions">
      {#if onpassword}<button type="button" class="btn btn-ghost" onclick={onpassword}>{t('cloud.signIn.passwordInstead')}</button>{/if}
      {#if deviceAvailable && !isWeb}<button type="button" class="btn btn-ghost" onclick={() => start('device')}>{t('cloud.signIn.useDevice')}</button>{/if}
      <button type="button" class="btn btn-primary" onclick={() => start()} data-autofocus><LogIn size={15} /> {t('cloud.signIn.with', { provider: displayName })}</button>
    </div>
  {:else}
    <div class="waiting" aria-live="polite">
      <p><LoaderCircle size={16} class="spin" /> {t('cloud.signIn.waiting')}</p>
      {#if flow.userCode}
        <p>{t('cloud.signIn.deviceCode', { url: flow.verificationUri ?? '' })}</p>
        <p class="code">{flow.userCode}</p>
      {:else if flow.authUrl}
        <a href={flow.authUrl} target="_blank" rel="noopener noreferrer"><ExternalLink size={13} /> {t('cloud.signIn.openLink')}</a>
      {/if}
      <div class="row-actions">
        <button type="button" class="btn btn-ghost" onclick={cancel}>{t('cloud.signIn.cancel')}</button>
      </div>
    </div>
  {/if}
</div>

<style>
  .oauth {
    display: flex;
    flex-direction: column;
    gap: 10px;
    margin-top: 8px;
  }
  .lead {
    margin: 0;
    color: var(--tm-muted);
    font-size: 13.5px;
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    flex-wrap: wrap;
  }
  .waiting p {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 0 6px;
  }
  .waiting a {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: 13px;
  }
  .code {
    font-family: var(--tm-mono, monospace);
    font-size: 22px;
    letter-spacing: 0.15em;
    font-weight: 700;
  }
</style>
