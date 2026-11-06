<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings -> Security: change the password and manage the sessions of the
  account (web client in server mode). Changing the password ends all other
  sessions (server behaviour). Accounts without local password (identity
  provider only) set a first password with an authenticator code instead of
  the current password.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import Monitor from '@lucide/svelte/icons/monitor';
  import Smartphone from '@lucide/svelte/icons/smartphone';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import LogOut from '@lucide/svelte/icons/log-out';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import { app } from '../../state';
  import type { Api } from '../../api/api';
  import type { AuthMe, AuthSession } from '../../api/security';
  import { fmt, t } from '../../i18n/index.svelte';
  import { buildPasswordChange, describeSession, securityErrorMessage, sortSessions, validatePasswordForm } from '../../security';

  interface Props {
    /** Defaults to the application API. */
    api?: Api;
  }
  let { api: apiProp }: Props = $props();
  const api = $derived(apiProp ?? app.api);

  /** Used until /api/auth/config answers (server default is 12). */
  const FALLBACK_MIN = 12;

  let me = $state<AuthMe | null>(null);
  let minLength = $state(FALLBACK_MIN);
  let sessions = $state<AuthSession[]>([]);
  let loadError = $state<string | null>(null);
  let sessionsError = $state<string | null>(null);

  let current = $state('');
  let next = $state('');
  let repeat = $state('');
  let secondFactor = $state('');
  let pwError = $state<string | null>(null);
  let pwBusy = $state(false);
  let sessionBusy = $state<number | 'others' | null>(null);

  const hasPassword = $derived(me?.user.hasPassword ?? true);
  const totpEnabled = $derived(!!me?.user.totpEnabled);
  const others = $derived(sessions.filter((s) => !s.current));
  const sorted = $derived(sortSessions(sessions));

  async function load() {
    loadError = null;
    try {
      me = await api.getAuthMe();
    } catch (err) {
      loadError = securityErrorMessage(err, t);
      return;
    }
    api.getAuthConfig().then(
      (c) => (minLength = c.passwordMinLength > 0 ? c.passwordMinLength : FALLBACK_MIN),
      () => undefined,
    );
    await loadSessions();
  }

  async function loadSessions() {
    sessionsError = null;
    try {
      sessions = await api.listAuthSessions();
    } catch (err) {
      sessionsError = securityErrorMessage(err, t);
    }
  }

  onMount(() => void load());

  async function changePassword(e: SubmitEvent) {
    e.preventDefault();
    if (pwBusy) return;
    const form = { hasPassword, current, next, repeat, secondFactor, totpEnabled, minLength };
    const problem = validatePasswordForm(form);
    if (problem) {
      pwError = t(problem.key, problem.params);
      return;
    }
    pwBusy = true;
    pwError = null;
    try {
      await api.changePassword(buildPasswordChange(form));
      current = next = repeat = secondFactor = '';
      app.toast(t(hasPassword ? 'security.password.changed' : 'security.password.setDone'));
      await load(); // hasPassword may have changed; other sessions are gone
    } catch (err) {
      pwError = securityErrorMessage(err, t);
    } finally {
      pwBusy = false;
    }
  }

  async function revoke(s: AuthSession) {
    sessionBusy = s.id;
    sessionsError = null;
    try {
      await api.revokeAuthSession(s.id);
      sessions = sessions.filter((x) => x.id !== s.id);
      app.toast(t('security.sessions.revoked'));
    } catch (err) {
      sessionsError = securityErrorMessage(err, t);
    } finally {
      sessionBusy = null;
    }
  }

  async function revokeOthers() {
    sessionBusy = 'others';
    sessionsError = null;
    try {
      await api.revokeOtherSessions();
      sessions = sessions.filter((x) => x.current);
      app.toast(t('security.sessions.othersRevoked'));
    } catch (err) {
      sessionsError = securityErrorMessage(err, t);
    } finally {
      sessionBusy = null;
    }
  }

  function title(s: AuthSession): string {
    const d = describeSession(s);
    if (!d.device) return t('security.sessions.unknownDevice');
    return d.os ? t('security.sessions.deviceOn', { device: d.device, os: d.os }) : d.device;
  }
</script>

<section class="card" id="set-password" aria-labelledby="set-password-h">
  <h2 id="set-password-h">{hasPassword ? t('security.password.title') : t('security.password.titleFirst')}</h2>
  <p class="intro">{hasPassword ? t('security.password.intro') : t('security.password.introFirst')}</p>

  {#if loadError}
    <p class="error" role="alert">{loadError}</p>
    <button class="btn" onclick={() => void load()}>{t('security.setup.retry')}</button>
  {:else if !me}
    <p class="muted"><LoaderCircle size={15} class="spin" /> {t('security.twofactor.loading')}</p>
  {:else}
    <form class="pw" onsubmit={changePassword}>
      {#if hasPassword}
        <label>
          <span class="label">{t('security.password.current')}</span>
          <input class="input" type="password" bind:value={current} autocomplete="current-password" />
        </label>
      {:else if totpEnabled}
        <label>
          <span class="label">{t('security.stepup.code')}</span>
          <input class="input" bind:value={secondFactor} autocomplete="one-time-code" autocapitalize="off" spellcheck="false" />
          <span class="hint">{t('security.stepup.codeHint')}</span>
        </label>
      {/if}
      <label>
        <span class="label">{t('security.password.new')}</span>
        <input class="input" type="password" bind:value={next} autocomplete="new-password" aria-describedby="pw-policy" />
        <span class="hint" id="pw-policy">{t('security.password.policy', { count: minLength })}</span>
      </label>
      <label>
        <span class="label">{t('security.password.repeat')}</span>
        <input class="input" type="password" bind:value={repeat} autocomplete="new-password" />
      </label>
      {#if pwError}<p class="error" role="alert">{pwError}</p>{/if}
      <div>
        <button type="submit" class="btn btn-primary" disabled={pwBusy}>
          {#if pwBusy}<LoaderCircle size={14} class="spin" />{:else}<KeyRound size={14} />{/if}
          {hasPassword ? t('security.password.submit') : t('security.password.submitFirst')}
        </button>
      </div>
    </form>
  {/if}
</section>

<section class="card" id="set-sessions" aria-labelledby="set-sessions-h">
  <h2 id="set-sessions-h">{t('security.sessions.title')}</h2>
  <p class="intro">{t('security.sessions.intro')}</p>
  {#if sessionsError}<p class="error" role="alert">{sessionsError}</p>{/if}
  {#if sorted.length}
    <ul class="sessions" data-testid="sessions">
      {#each sorted as s (s.id)}
        <li class:current={s.current}>
          <span class="ic" aria-hidden="true">{#if s.kind === 'device'}<Smartphone size={18} />{:else}<Monitor size={18} />{/if}</span>
          <div class="info">
            <div class="name">
              {title(s)}
              {#if s.current}<span class="badge">{t('security.sessions.thisDevice')}</span>{/if}
              {#if s.kind === 'device'}<span class="badge badge-muted">{t('security.sessions.app')}</span>{/if}
            </div>
            <div class="meta">
              {t('security.sessions.lastActive', { when: s.current ? t('security.sessions.now') : fmt().relative(s.lastSeenAt) })}
              · {t('security.sessions.signedIn', { when: fmt().dateTime(s.createdAt) })}
              {#if s.ip}· {s.ip}{/if}
            </div>
          </div>
          {#if !s.current}
            <button class="btn" disabled={sessionBusy !== null} onclick={() => void revoke(s)} aria-label={t('security.sessions.revokeLabel', { name: title(s) })}>
              {#if sessionBusy === s.id}<LoaderCircle size={14} class="spin" />{:else}<LogOut size={14} />{/if}
              {t('security.sessions.revoke')}
            </button>
          {/if}
        </li>
      {/each}
    </ul>
    <div class="foot">
      <button class="btn" disabled={others.length === 0 || sessionBusy !== null} onclick={() => void revokeOthers()}>
        {#if sessionBusy === 'others'}<LoaderCircle size={14} class="spin" />{:else}<LogOut size={14} />{/if}
        {t('security.sessions.revokeOthers')}
      </button>
      {#if others.length === 0}<span class="hint">{t('security.sessions.noOthers')}</span>{/if}
    </div>
  {:else if !sessionsError}
    <p class="muted">{t('security.sessions.empty')}</p>
  {/if}
</section>

<style>
  .pw {
    display: flex;
    flex-direction: column;
    gap: 12px;
    max-width: 420px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .hint {
    font-size: 12px;
    color: var(--tm-faint);
  }
  .muted {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .error {
    margin: 0 0 8px;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .sessions {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .sessions li {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 4px;
    border-top: 1px solid var(--tm-line);
  }
  .ic {
    flex: none;
    color: var(--tm-muted);
    display: inline-flex;
  }
  .info {
    flex: 1;
    min-width: 0;
  }
  .name {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    font-size: 13.5px;
    font-weight: 600;
  }
  .meta {
    font-size: 12px;
    color: var(--tm-muted);
    overflow-wrap: anywhere;
  }
  .foot {
    display: flex;
    align-items: center;
    gap: 10px;
    padding-top: 10px;
    border-top: 1px solid var(--tm-line);
  }
  :global(.spin) {
    animation: spin 0.9s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (max-width: 520px) {
    .sessions li {
      flex-wrap: wrap;
    }
  }
</style>
