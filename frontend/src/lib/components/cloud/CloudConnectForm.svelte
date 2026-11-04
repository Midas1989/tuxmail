<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Connect a cloud account (KONZEPT 12.3-12.5): Nextcloud URL -> Login Flow v2,
  OpenCloud URL -> OpenID Connect in the browser, Vaultwarden URL + e-mail +
  master password (+ TOTP), WebDAV URL + user + password.
-->
<script lang="ts" module>
  import type { CloudAccountProvider } from '../../types';

  export const CLOUD_PROVIDERS: CloudAccountProvider[] = ['nextcloud', 'opencloud', 'vaultwarden', 'webdav'];

  /** Poll interval of browser logins. */
  export const POLL_MS = 2000;
</script>

<script lang="ts">
  import { onDestroy } from 'svelte';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import { app, cloud } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { isNativeShell, openExternal } from '../../platform';
  import type { CloudAccount, CloudLoginFlow, CloudLoginInput } from '../../types';
  import ProviderIcon from './ProviderIcon.svelte';

  interface Props {
    provider?: CloudAccountProvider;
    ondone: (acc: CloudAccount) => void;
    oncancel: () => void;
  }
  let { provider: initial = 'nextcloud', ondone, oncancel }: Props = $props();

  // svelte-ignore state_referenced_locally
  let provider = $state<CloudAccountProvider>(initial);
  let server = $state('');
  let displayName = $state('');
  let email = $state('');
  let username = $state('');
  let password = $state('');
  let totp = $state('');
  let needTotp = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let flow = $state<CloudLoginFlow | null>(null);
  let done = $state<CloudAccount | null>(null);
  let timer: ReturnType<typeof setTimeout> | null = null;

  onDestroy(() => {
    if (timer) clearTimeout(timer);
    if (flow?.status === 'pending') void app.api.cancelCloudLogin(flow.id).catch(() => {});
  });

  const browserLogin = $derived(provider === 'nextcloud' || provider === 'opencloud');
  const placeholders: Record<CloudAccountProvider, string> = {
    nextcloud: 'cloud.example.de',
    opencloud: 'opencloud.example.de',
    vaultwarden: 'vault.example.de',
    webdav: 'https://dav.example.de/remote.php/webdav/',
  };

  function redirectUrl(): string | undefined {
    if (provider !== 'opencloud' || app.api.kind !== 'http' || isNativeShell()) return undefined;
    return `${location.origin}/api/cloud/oauth/callback`;
  }

  async function start() {
    if (!server.trim()) {
      error = t('cloud.connect.errServer');
      return;
    }
    error = null;
    busy = true;
    const input: CloudLoginInput = { provider, server: server.trim(), displayName: displayName.trim() || undefined, redirectUrl: redirectUrl() };
    if (provider === 'vaultwarden') Object.assign(input, { email: email.trim(), password, totp: totp.trim() || undefined });
    if (provider === 'webdav') Object.assign(input, { username: username.trim(), password });
    try {
      const f = await app.api.startCloudLogin(input);
      handle(f);
      if (f.status === 'pending' && f.loginUrl) openExternal(f.loginUrl);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
      busy = false;
    }
  }

  function handle(f: CloudLoginFlow) {
    flow = f;
    if (f.status === 'done' && f.account) {
      busy = false;
      done = f.account;
      void cloud.load();
      return;
    }
    if (f.status === 'two_factor') {
      busy = false;
      needTotp = true;
      error = totp ? t('cloud.connect.errTotp') : null;
      return;
    }
    if (f.status === 'error') {
      busy = false;
      error = f.error ?? t('cloud.connect.failed');
      return;
    }
    timer = setTimeout(poll, POLL_MS);
  }

  async function poll() {
    timer = null;
    if (!flow) return;
    try {
      handle(await app.api.pollCloudLogin(flow.id));
    } catch (err) {
      busy = false;
      error = err instanceof Error ? err.message : String(err);
    }
  }

  function cancel() {
    if (timer) clearTimeout(timer);
    timer = null;
    if (flow?.status === 'pending') void app.api.cancelCloudLogin(flow.id).catch(() => {});
    flow = null;
    busy = false;
    oncancel();
  }
</script>

{#if done}
  <div class="done">
    <p class="ok"><CircleCheck size={20} /> {t('cloud.connect.done', { name: done.displayName })}</p>
    {#if done.dav}
      <div class="dav">
        <strong>{t('files.dav.title')}</strong>
        <p>{t('files.dav.hint')}</p>
        <code>{done.dav.caldav}</code>
        <code>{done.dav.carddav}</code>
      </div>
    {/if}
    <div class="row-actions">
      <button class="btn btn-primary" onclick={() => ondone(done!)} data-autofocus>{t('cloud.connect.finish')}</button>
    </div>
  </div>
{:else if flow?.status === 'pending'}
  <div class="waiting" aria-live="polite">
    <LoaderCircle size={28} class="spin" />
    <p><strong>{t('cloud.connect.waiting')}</strong></p>
    <p class="hint">{provider === 'nextcloud' ? t('cloud.connect.loginFlowHint') : t('cloud.connect.oidcHint')}</p>
    <div class="row-actions">
      <button class="btn btn-ghost" onclick={cancel}>{t('common.action.cancel')}</button>
      <button class="btn" onclick={() => flow?.loginUrl && openExternal(flow.loginUrl)}><ExternalLink size={15} /> {t('cloud.connect.openAgain')}</button>
    </div>
  </div>
{:else}
  <form
    class="form"
    onsubmit={(e) => {
      e.preventDefault();
      void start();
    }}
  >
    <div class="providers" role="radiogroup" aria-label={t('cloud.connect.provider')}>
      {#each CLOUD_PROVIDERS as p (p)}
        <button type="button" role="radio" aria-checked={provider === p} class="prov" class:active={provider === p} onclick={() => ((provider = p), (needTotp = false), (error = null))}>
          <ProviderIcon provider={p} size={18} />
          <span>{t(`cloud.provider.${p}`)}</span>
        </button>
      {/each}
    </div>
    <p class="hint">{t(`cloud.connect.lead.${provider}`)}</p>
    <label>
      <span class="label">{provider === 'webdav' ? t('cloud.connect.webdavUrl') : t('cloud.connect.server')}</span>
      <input class="input" bind:value={server} placeholder={placeholders[provider]} inputmode="url" autocomplete="url" data-autofocus required />
    </label>
    {#if provider === 'vaultwarden'}
      <label><span class="label">{t('cloud.connect.email')}</span><input class="input" type="email" bind:value={email} autocomplete="username" required /></label>
      <label><span class="label">{t('cloud.connect.masterPassword')}</span><input class="input" type="password" bind:value={password} autocomplete="current-password" required /></label>
      {#if needTotp}
        <label>
          <span class="label">{t('cloud.connect.totp')}</span>
          <input class="input" bind:value={totp} inputmode="numeric" autocomplete="one-time-code" maxlength="10" required />
        </label>
        <p class="hint">{t('cloud.connect.totpHint')}</p>
      {/if}
    {:else if provider === 'webdav'}
      <label><span class="label">{t('cloud.connect.username')}</span><input class="input" bind:value={username} autocomplete="username" /></label>
      <label><span class="label">{t('cloud.connect.password')}</span><input class="input" type="password" bind:value={password} autocomplete="current-password" /></label>
    {/if}
    <label><span class="label">{t('cloud.connect.displayName')}</span><input class="input" bind:value={displayName} placeholder={t('cloud.connect.displayNameHint')} /></label>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <div class="row-actions">
      <button type="button" class="btn btn-ghost" onclick={cancel}>{t('common.action.cancel')}</button>
      <button type="submit" class="btn btn-primary" disabled={busy}>
        {#if busy}<LoaderCircle size={15} class="spin" />{/if}
        {browserLogin ? t('cloud.connect.signInBrowser') : t('cloud.connect.signIn')}
      </button>
    </div>
  </form>
{/if}

<style>
  .form,
  .done,
  .waiting {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .waiting {
    align-items: center;
    text-align: center;
    padding: 12px 0;
  }
  .waiting p {
    margin: 0;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .label {
    font-size: 12.5px;
    font-weight: 600;
    color: var(--tm-muted);
  }
  .providers {
    display: grid;
    grid-template-columns: repeat(4, 1fr);
    gap: 8px;
  }
  .prov {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 10px 6px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface);
    font-size: 12.5px;
    color: var(--tm-fg);
  }
  .prov.active {
    border-color: var(--tm-accent);
    box-shadow: 0 0 0 1px var(--tm-accent);
    background: var(--tm-accent-soft);
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
  .ok {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    color: var(--tm-success);
    font-weight: 600;
  }
  .dav {
    display: flex;
    flex-direction: column;
    gap: 4px;
    padding: 10px 12px;
    border-radius: var(--tm-radius);
    background: var(--tm-surface-2);
    font-size: 12.5px;
  }
  .dav p {
    margin: 0;
    color: var(--tm-muted);
  }
  .dav code {
    font-size: 11.5px;
    word-break: break-all;
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 4px;
  }
  @media (max-width: 479px) {
    .providers {
      grid-template-columns: repeat(2, 1fr);
    }
  }
</style>
