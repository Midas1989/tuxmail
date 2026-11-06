<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Security: passkeys (WebAuthn) of the signed-in account in the web
  client served by `tuxmail serve`. Register (name + re-authentication), list,
  rename, remove. The logic is in lib/passkeys/manager.ts (unit-tested); this
  component only holds the state. Hidden where there is no server account
  (desktop shell, demo) or in the native app (which signs in via the browser).
-->
<script lang="ts">
  import KeyRound from '@lucide/svelte/icons/key-round';
  import Pencil from '@lucide/svelte/icons/pencil';
  import Trash from '@lucide/svelte/icons/trash';
  import Plus from '@lucide/svelte/icons/plus';
  import { app } from '../../state';
  import { isNativeShell } from '../../platform';
  import { t, formatFullDate, type MessageKey } from '../../i18n/index.svelte';
  import { createPasskeyClient, type PasskeyEntry, type PasskeyListing } from '../../passkeys/client';
  import { PasskeyManager, MAX_PASSKEY_NAME } from '../../passkeys/manager';
  import type { PasskeyErrorKind, WebAuthnEnv } from '../../passkeys/webauthn';

  const client = isNativeShell() ? null : createPasskeyClient(app.api);
  const manager = client
    ? new PasskeyManager({ client, credentials: typeof navigator === 'undefined' ? undefined : navigator.credentials, env: window as unknown as WebAuthnEnv })
    : null;

  let listing = $state<PasskeyListing | null>(null);
  let loadError = $state<PasskeyErrorKind | null>(null);
  let busy = $state(false);
  let error = $state<PasskeyErrorKind | null>(null);

  // add form
  let newName = $state('');
  let addPassword = $state('');
  let addCode = $state('');

  // rename (one row at a time)
  let renaming = $state<number | null>(null);
  let renameValue = $state('');

  // remove (one row at a time)
  let removing = $state<number | null>(null);
  let removePassword = $state('');
  let removeCode = $state('');

  const errorText = (kind: PasskeyErrorKind) => t(`settings.passkeys.errors.${kind}` as MessageKey);

  async function load() {
    if (!manager) return;
    const r = await manager.load();
    if (r.ok) {
      listing = r.value;
      loadError = null;
    } else {
      loadError = r.kind;
    }
  }
  void load();

  async function add(e: Event) {
    e.preventDefault();
    if (!manager || !listing || busy) return;
    busy = true;
    error = null;
    const r = await manager.register(listing, newName, { password: addPassword, code: addCode }, t('settings.passkeys.add.defaultName'));
    busy = false;
    if (!r.ok) {
      // The password and the one-time code are spent once the server saw them.
      addCode = '';
      error = r.kind;
      return;
    }
    app.toast(t('settings.passkeys.done.added', { name: r.value.name }));
    newName = addPassword = addCode = '';
    await load();
  }

  function startRename(p: PasskeyEntry) {
    renaming = p.id;
    renameValue = p.name;
    removing = null;
  }

  async function saveRename(e: Event) {
    e.preventDefault();
    if (!manager || renaming === null || busy) return;
    busy = true;
    error = null;
    const r = await manager.rename(renaming, renameValue);
    busy = false;
    if (!r.ok) {
      error = r.kind;
      return;
    }
    renaming = null;
    app.toast(t('settings.passkeys.done.renamed'));
    await load();
  }

  function startRemove(p: PasskeyEntry) {
    removing = p.id;
    removePassword = removeCode = '';
    renaming = null;
    error = null;
  }

  async function confirmRemove(e: Event) {
    e.preventDefault();
    if (!manager || !listing || removing === null || busy) return;
    busy = true;
    error = null;
    const r = await manager.remove(listing, removing, { password: removePassword, code: removeCode });
    busy = false;
    removeCode = '';
    if (!r.ok) {
      error = r.kind;
      return;
    }
    removing = null;
    removePassword = '';
    app.toast(t('settings.passkeys.done.removed'));
    await load();
  }

  const removeTarget = $derived(listing?.passkeys.find((p) => p.id === removing));
</script>

{#if manager && (listing?.enabled || (listing?.passkeys.length ?? 0) > 0 || loadError)}
  <section class="card" id="set-passkeys" aria-labelledby="set-passkeys-h">
    <h2 id="set-passkeys-h"><KeyRound size={15} /> {t('settings.passkeys.title')}</h2>
    <p class="intro">{t('settings.passkeys.intro')}</p>
    <p class="note">{t('settings.passkeys.twoFactorNote')}</p>

    {#if loadError}
      <p class="err" role="alert">{errorText(loadError)}</p>
    {:else if !listing}
      <p class="muted">{t('settings.passkeys.loading')}</p>
    {:else}
      {#if listing.passkeys.length === 0}
        <p class="muted">{t('settings.passkeys.empty')}</p>
      {:else}
        <ul class="keys">
          {#each listing.passkeys as p (p.id)}
            <li class:stale={!p.usable}>
              <div class="line">
                {#if renaming === p.id}
                  <form class="inline" onsubmit={saveRename}>
                    <input class="input" bind:value={renameValue} maxlength={MAX_PASSKEY_NAME} aria-label={t('settings.passkeys.renameLabel', { name: p.name })} />
                    <button class="btn btn-primary" type="submit" disabled={busy}>{t('settings.passkeys.save')}</button>
                    <button class="btn btn-ghost" type="button" onclick={() => (renaming = null)}>{t('common.action.cancel')}</button>
                  </form>
                {:else}
                  <div class="what">
                    <strong>{p.name}</strong>
                    <span class="meta">
                      {t('settings.passkeys.created', { date: formatFullDate(p.createdAt) })} ·
                      {p.lastUsedAt ? t('settings.passkeys.lastUsed', { date: formatFullDate(p.lastUsedAt) }) : t('settings.passkeys.neverUsed')}
                    </span>
                    <span class="tags">
                      <span class="tag">{p.backupEligible ? t('settings.passkeys.synced') : t('settings.passkeys.deviceBound')}</span>
                      {#if !p.usable}<span class="tag warn">{t('settings.passkeys.stale')}</span>{/if}
                    </span>
                    {#if !p.usable}<span class="meta">{t('settings.passkeys.staleHint')}</span>{/if}
                  </div>
                  <div class="actions">
                    <button class="icon-btn" aria-label={t('settings.passkeys.rename') + ': ' + p.name} title={t('settings.passkeys.rename')} onclick={() => startRename(p)}><Pencil size={15} /></button>
                    <button class="icon-btn" aria-label={t('settings.passkeys.remove.action') + ': ' + p.name} title={t('settings.passkeys.remove.action')} onclick={() => startRemove(p)}><Trash size={15} /></button>
                  </div>
                {/if}
              </div>
            </li>
          {/each}
        </ul>
      {/if}

      {#if removeTarget}
        <form class="panel" onsubmit={confirmRemove}>
          <h3>{t('settings.passkeys.remove.title', { name: removeTarget.name })}</h3>
          <p class="muted">{t('settings.passkeys.remove.text')}</p>
          {#if listing.reauth.password}
            <label class="label" for="pk-rm-pw">{t('settings.passkeys.remove.password')}</label>
            <input id="pk-rm-pw" class="input" type="password" autocomplete="current-password" bind:value={removePassword} />
          {/if}
          {#if listing.reauth.code}
            <label class="label" for="pk-rm-code">{t('settings.passkeys.remove.code')}</label>
            <input id="pk-rm-code" class="input" inputmode="text" autocomplete="one-time-code" bind:value={removeCode} />
          {/if}
          <div class="buttons">
            <button class="btn btn-primary" type="submit" disabled={busy}>{t('settings.passkeys.remove.confirm')}</button>
            <button class="btn btn-ghost" type="button" onclick={() => (removing = null)}>{t('common.action.cancel')}</button>
          </div>
        </form>
      {/if}

      {#if listing.enabled}
        {#if !manager.supported}
          <p class="muted">{t('settings.passkeys.unsupported')}</p>
        {:else if listing.passkeys.length >= listing.max}
          <p class="muted">{t('settings.passkeys.add.limit', { max: listing.max })}</p>
        {:else}
          <form class="panel" onsubmit={add}>
            <h3>{t('settings.passkeys.add.title')}</h3>
            <label class="label" for="pk-name">{t('settings.passkeys.add.name')}</label>
            <input id="pk-name" class="input" bind:value={newName} maxlength={MAX_PASSKEY_NAME} placeholder={t('settings.passkeys.add.namePlaceholder')} autocomplete="off" />
            {#if listing.reauth.password}
              <label class="label" for="pk-pw">{t('settings.passkeys.add.password')}</label>
              <input id="pk-pw" class="input" type="password" autocomplete="current-password" bind:value={addPassword} />
            {/if}
            {#if listing.reauth.code}
              <label class="label" for="pk-code">{t('settings.passkeys.add.code')}</label>
              <input id="pk-code" class="input" inputmode="text" autocomplete="one-time-code" bind:value={addCode} />
            {/if}
            <div class="buttons">
              <button class="btn btn-primary" type="submit" disabled={busy}>
                <Plus size={14} />
                {busy ? t('settings.passkeys.add.waiting') : t('settings.passkeys.add.submit')}
              </button>
            </div>
          </form>
        {/if}
      {:else}
        <p class="muted">{t('settings.passkeys.disabledByServer')}</p>
      {/if}

      {#if error}<p class="err" role="alert">{errorText(error)}</p>{/if}
    {/if}
  </section>
{/if}

<style>
  h2 {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  h3 {
    margin: 0 0 6px;
    font-size: 13.5px;
    font-weight: 650;
  }
  .note,
  .muted,
  .meta {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .note {
    margin: 0 0 10px;
  }
  .muted {
    margin: 8px 0;
  }
  .err {
    margin: 10px 0 0;
    font-size: 13px;
    color: var(--tm-danger);
  }
  .keys {
    list-style: none;
    margin: 8px 0;
    padding: 0;
  }
  .keys li {
    padding: 10px 0;
    border-top: 1px solid var(--tm-line);
  }
  .keys li.stale {
    opacity: 0.85;
  }
  .line {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 10px;
  }
  .what {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .actions {
    display: flex;
    gap: 2px;
  }
  .tags {
    display: flex;
    gap: 6px;
    margin-top: 2px;
  }
  .tag {
    padding: 1px 8px;
    border-radius: 99px;
    font-size: 11.5px;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
  }
  .tag.warn {
    color: var(--tm-danger);
  }
  .inline {
    display: flex;
    gap: 6px;
    flex: 1;
    flex-wrap: wrap;
  }
  .inline .input {
    flex: 1;
    min-width: 160px;
  }
  .panel {
    margin-top: 14px;
    padding: 12px 14px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface-2);
  }
  .panel .label {
    margin-top: 8px;
  }
  .buttons {
    display: flex;
    gap: 8px;
    margin-top: 12px;
  }
</style>
