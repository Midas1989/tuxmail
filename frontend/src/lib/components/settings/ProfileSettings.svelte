<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → My account (web client in server mode): the own display name,
  username and e-mail address for reference, the way to the security cards and
  sign-out (on phones this page is the only place next to Security for it).
  Username and e-mail address are login names: read-only here. Accounts linked
  to an identity provider keep the name it delivers (see auth.UpdateOwnProfile).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import LogOut from '@lucide/svelte/icons/log-out';
  import { app, security } from '../../state';
  import { signOut } from '../../accountActions';
  import { t, type MessageKey } from '../../i18n/index.svelte';
  import { securityErrorMessage } from '../../security';

  interface Props {
    part: 'profile' | 'signin' | 'session';
  }
  let { part }: Props = $props();

  /** Same limit as the server (auth.MaxDisplayNameLength). */
  const MAX_NAME = 100;

  /** Cards of Settings → Security (their section is one long page). */
  const securityLinks: { label: MessageKey; card: string }[] = [
    { label: 'security.password.title', card: 'set-password' },
    { label: 'security.twofactor.title', card: 'set-twofactor' },
    { label: 'settings.passkeys.title', card: 'set-passkeys' },
    { label: 'security.sessions.title', card: 'set-sessions' },
  ];

  const user = $derived(security.me?.user ?? null);

  /** null: the field shows the saved name. */
  let draft = $state<string | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);

  const value = $derived(draft ?? user?.displayName ?? '');
  const changed = $derived(value.trim() !== (user?.displayName ?? ''));

  onMount(() => {
    if (part === 'profile' && !security.me) void security.refresh();
  });

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (busy || !changed) return;
    const name = value.trim();
    if (!name) {
      error = t('settings.pages.profile.required');
      return;
    }
    busy = true;
    error = null;
    try {
      await security.updateProfile({ displayName: name });
      draft = null;
      app.toast(t('settings.pages.profile.saved'));
    } catch (err) {
      const code = (err as { code?: unknown } | null)?.code;
      if (code === 'invalid_input') error = t('settings.pages.profile.invalid', { max: MAX_NAME });
      else if (code === 'managed_by_idp') error = t('settings.pages.profile.oidcHint');
      else error = securityErrorMessage(err, t);
    } finally {
      busy = false;
    }
  }

  function openCard(card: string) {
    app.openSettings('security', 'privacy');
    // After the settings page has scrolled to the section (SettingsScreen.settle).
    setTimeout(() => document.getElementById(card)?.scrollIntoView({ block: 'start' }), 350);
  }
</script>

{#if part === 'profile'}
  <section class="card" id="set-profile" aria-labelledby="set-profile-h">
    <h2 id="set-profile-h">{t('settings.pages.profile.sections.profile')}</h2>
    {#if user}
      {#if user.oidc}
        <dl class="facts">
          <div>
            <dt>{t('settings.pages.profile.displayName')}</dt>
            <dd>{user.displayName || '–'}</dd>
          </div>
          {@render loginFacts(user.username, user.email)}
        </dl>
        <p class="hint">{t('settings.pages.profile.oidcHint')}</p>
      {:else}
        <form class="name" onsubmit={save} novalidate>
          <label class="label" for="profile-name">{t('settings.pages.profile.displayName')}</label>
          <input
            id="profile-name"
            class="input"
            type="text"
            autocomplete="name"
            maxlength={MAX_NAME}
            {value}
            oninput={(e) => {
              draft = e.currentTarget.value;
              error = null;
            }}
            aria-invalid={error ? 'true' : undefined}
            aria-describedby={error ? 'profile-name-error' : 'profile-name-hint'}
          />
          {#if error}
            <p class="error" id="profile-name-error" role="alert">{error}</p>
          {:else}
            <p class="hint" id="profile-name-hint">{t('settings.pages.profile.displayNameHint', { max: MAX_NAME })}</p>
          {/if}
          <div>
            <button class="btn btn-primary" type="submit" disabled={busy || !changed}>{t('common.action.save')}</button>
          </div>
        </form>
        <dl class="facts">
          {@render loginFacts(user.username, user.email)}
        </dl>
        <p class="hint">{t('settings.pages.profile.loginHint')}</p>
      {/if}
    {/if}
  </section>
{:else if part === 'signin'}
  <section class="card" id="set-profile-security" aria-labelledby="set-profile-security-h">
    <h2 id="set-profile-security-h">{t('settings.pages.profile.sections.signin')}</h2>
    <ul class="links">
      {#each securityLinks as l (l.card)}
        <li>
          <button class="link-row" onclick={() => openCard(l.card)}>
            <span>{t(l.label)}</span>
            <ChevronRight size={15} aria-hidden="true" />
          </button>
        </li>
      {/each}
    </ul>
  </section>
{:else}
  <section class="card" id="set-profile-session" aria-labelledby="set-profile-session-h">
    <h2 id="set-profile-session-h">{t('settings.pages.profile.sections.session')}</h2>
    <p class="intro">{t('settings.pages.session.intro')}</p>
    <button class="btn" onclick={() => void signOut()}><LogOut size={14} aria-hidden="true" /> {t('settings.pages.session.signOut')}</button>
  </section>
{/if}

{#snippet loginFacts(username: string, email: string)}
  <div>
    <dt>{t('settings.pages.profile.username')}</dt>
    <dd>{username}</dd>
  </div>
  <div>
    <dt>{t('settings.pages.profile.email')}</dt>
    <dd>{email || t('settings.pages.profile.noEmail')}</dd>
  </div>
{/snippet}

<style>
  .name {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-width: 420px;
    margin-bottom: 16px;
  }
  .name > div {
    margin-top: 4px;
  }
  .hint {
    margin: 0;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .error {
    margin: 0;
    font-size: 13px;
    color: var(--tm-danger);
  }
  .facts {
    margin: 0 0 6px;
  }
  .facts > div {
    display: flex;
    flex-wrap: wrap;
    gap: 2px 16px;
    padding: 8px 0;
    border-top: 1px solid var(--tm-line);
  }
  dt {
    width: 130px;
    flex: none;
    font-size: 13px;
    color: var(--tm-muted);
  }
  dd {
    margin: 0;
    min-width: 0;
    font-size: 13.5px;
    overflow-wrap: anywhere;
  }
  .links {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .links li + li {
    border-top: 1px solid var(--tm-line);
  }
  .link-row {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    min-height: 44px;
    padding: 6px 4px;
    background: none;
    border: none;
    border-radius: 8px;
    font: inherit;
    font-size: 13.5px;
    color: inherit;
    text-align: left;
    cursor: pointer;
  }
  .link-row span {
    flex: 1;
    min-width: 0;
  }
  .link-row:hover,
  .link-row:focus-visible {
    background: var(--tm-surface-2);
  }
</style>
