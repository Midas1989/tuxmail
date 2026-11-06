<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Create a user (user name, e-mail address, display name, role, initial
  password or "generate") or edit one (display name, e-mail address, role).
  The password policy of the server is shown as a hint and its errors are
  reported in plain words; a generated password is handed to the caller, which
  shows it once.
-->
<script lang="ts">
  import Modal from '../common/Modal.svelte';
  import { t } from '../../i18n/index.svelte';
  import { ADMIN_ROLES, type AdminClient, type AdminRole, type AdminUser } from '../../api/admin';
  import { adminErrorMessage } from '../../admin/errors';
  import type { GuardReason } from '../../admin/users';

  interface Props {
    client: AdminClient;
    /** The user to edit; null creates one. */
    user: AdminUser | null;
    /** Server password policy (0 = unknown). */
    minPasswordLength?: number;
    /** Why the role cannot be changed (own account, last administrator). */
    roleLocked?: GuardReason | null;
    onsaved: (r: { user: AdminUser; password?: string; created: boolean }) => void;
    onclose: () => void;
  }
  let { client, user, minPasswordLength = 0, roleLocked = null, onsaved, onclose }: Props = $props();

  // The form edits a copy; `user` is only the starting point.
  // svelte-ignore state_referenced_locally
  let username = $state(user?.username ?? '');
  // svelte-ignore state_referenced_locally
  let displayName = $state(user?.displayName ?? '');
  // svelte-ignore state_referenced_locally
  let email = $state(user?.email ?? '');
  // svelte-ignore state_referenced_locally
  let role = $state<AdminRole>(user?.role ?? 'user');
  let passwordMode = $state<'generate' | 'custom'>('generate');
  let password = $state('');
  let busy = $state(false);
  let error = $state('');

  const editing = $derived(user !== null);
  const valid = $derived(
    (editing || /^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$/.test(username.trim())) && (editing || passwordMode === 'generate' || password.length > 0),
  );

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy || !valid) return;
    busy = true;
    error = '';
    try {
      if (user) {
        const patch: { displayName?: string; email?: string; role?: AdminRole } = {};
        if (displayName.trim() !== user.displayName) patch.displayName = displayName.trim();
        if (email.trim() !== user.email) patch.email = email.trim();
        if (role !== user.role && !roleLocked) patch.role = role;
        const saved = Object.keys(patch).length ? await client.updateUser(user.id, patch) : user;
        onsaved({ user: saved, created: false });
      } else {
        const r = await client.createUser({
          username: username.trim(),
          email: email.trim(),
          displayName: displayName.trim(),
          role,
          password: passwordMode === 'custom' ? password : undefined,
        });
        onsaved({ user: r.user, password: r.password, created: true });
      }
    } catch (err) {
      error = adminErrorMessage(err, t, minPasswordLength);
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={editing ? t('admin.form.editTitle', { name: user?.username ?? '' }) : t('admin.form.createTitle')} {onclose} width="520px">
  <form id="admin-user-form" onsubmit={submit} class="form">
    <div class="field">
      <label class="label" for="au-username">{t('admin.form.username')}</label>
      <input id="au-username" class="input" bind:value={username} readonly={editing} required autocomplete="off" autocapitalize="none" spellcheck="false" maxlength="64" data-autofocus={editing ? undefined : true} />
      {#if !editing}<p class="hint">{t('admin.form.usernameHint')}</p>{/if}
    </div>
    <div class="field">
      <label class="label" for="au-name">{t('admin.form.displayName')}</label>
      <input id="au-name" class="input" bind:value={displayName} autocomplete="off" maxlength="100" data-autofocus={editing ? true : undefined} />
    </div>
    <div class="field">
      <label class="label" for="au-email">{t('admin.form.email')}</label>
      <input id="au-email" class="input" type="email" bind:value={email} autocomplete="off" />
    </div>
    <div class="field">
      <label class="label" for="au-role">{t('admin.form.role')}</label>
      <select id="au-role" class="input" bind:value={role} disabled={!!roleLocked} aria-describedby="au-role-hint">
        {#each ADMIN_ROLES as r (r)}<option value={r}>{t(`admin.role.${r}`)}</option>{/each}
      </select>
      <p class="hint" id="au-role-hint">
        {#if roleLocked === 'self'}{t('admin.guard.roleSelf')}{:else if roleLocked === 'lastAdmin'}{t('admin.guard.roleLastAdmin')}{:else}{t(`admin.roleHint.${role}`)}{/if}
      </p>
    </div>
    {#if !editing}
      <fieldset class="field pw">
        <legend class="label">{t('admin.form.password')}</legend>
        <label class="radio"><input type="radio" name="au-pw" value="generate" bind:group={passwordMode} /> {t('admin.form.passwordGenerate')}</label>
        <label class="radio"><input type="radio" name="au-pw" value="custom" bind:group={passwordMode} /> {t('admin.form.passwordCustom')}</label>
        {#if passwordMode === 'custom'}
          <input id="au-password" class="input" type="password" bind:value={password} autocomplete="new-password" aria-label={t('admin.form.passwordLabel')} />
          <p class="hint">{minPasswordLength ? t('admin.form.passwordPolicy', { min: minPasswordLength }) : t('admin.form.passwordPolicyUnknown')}</p>
        {:else}
          <p class="hint">{t('admin.form.passwordGenerateHint')}</p>
        {/if}
      </fieldset>
    {/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  </form>
  {#snippet footer()}
    <button class="btn" type="button" onclick={onclose}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary" type="submit" form="admin-user-form" disabled={busy || !valid} data-testid="user-form-submit">{editing ? t('common.action.save') : t('admin.form.create')}</button>
  {/snippet}
</Modal>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .field {
    margin: 0;
    padding: 0;
    border: none;
    min-width: 0;
  }
  .hint {
    margin: 4px 0 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .radio {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 4px 0;
    font-size: 14px;
  }
  .pw .input {
    margin-top: 6px;
  }
  .error {
    margin: 0;
    padding: 8px 10px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-danger) 12%, transparent);
    color: var(--tm-danger);
    font-size: 13px;
  }
</style>
