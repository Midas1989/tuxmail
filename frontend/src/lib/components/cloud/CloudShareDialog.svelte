<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Create a public link (Nextcloud OCS share, OpenCloud createLink) or a
  Vaultwarden Send (file or text) with password, expiry and options
  (KONZEPT 12.2, 12.5).
-->
<script lang="ts" module>
  /** Default share lifetime in days (KONZEPT 12.2). */
  export const DEFAULT_EXPIRY_DAYS = 14;

  export function dateInput(d: Date): string {
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }

  /** End of the chosen local day as ISO string. */
  export function endOfDay(value: string): string {
    const [y, m, d] = value.split('-').map(Number);
    return new Date(y, m - 1, d, 23, 59, 59).toISOString();
  }
</script>

<script lang="ts">
  import Copy from '@lucide/svelte/icons/copy';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import { app } from '../../state';
  import { formatFullDate, t } from '../../i18n/index.svelte';
  import type { CloudAccount, CloudEntry, CloudShare } from '../../types';
  import Modal from '../common/Modal.svelte';

  interface Props {
    account: CloudAccount;
    /** Stored file to share (Nextcloud/OpenCloud). */
    entry?: CloudEntry;
    /** Vaultwarden: a local file to send (else a text Send). */
    file?: File;
    onclose: () => void;
    oncreated?: (s: CloudShare) => void;
  }
  let { account, entry, file, onclose, oncreated }: Props = $props();

  const isSend = $derived(account.capabilities.send);
  let usePassword = $state(true);
  let password = $state('');
  let expires = $state(dateInput(new Date(Date.now() + DEFAULT_EXPIRY_DAYS * 86400_000)));
  let note = $state('');
  let text = $state('');
  let maxAccess = $state<number | null>(null);
  let hideEmail = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let created = $state<CloudShare | null>(null);

  const title = $derived(entry ? t('cloud.share.title', { name: entry.name }) : file ? t('cloud.share.title', { name: file.name }) : t('cloud.share.textTitle'));
  const minDate = dateInput(new Date(Date.now() + 86400_000));

  async function generate() {
    try {
      password = (await app.api.generateCloudPassword(account.id)).password;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function create() {
    error = null;
    if (isSend && !file && !text.trim()) {
      error = t('cloud.share.errText');
      return;
    }
    busy = true;
    try {
      let uploadToken: string | undefined;
      if (isSend && file) uploadToken = (await app.api.uploadAttachment(file, file.name)).uploadToken;
      const s = await app.api.createCloudShare({
        accountId: account.id,
        path: entry?.path,
        uploadToken,
        text: isSend && !file ? text : undefined,
        name: file?.name,
        password: usePassword && password ? password : undefined,
        generatePassword: usePassword && !password,
        expiresAt: endOfDay(expires),
        note: note.trim() || undefined,
        maxAccessCount: isSend && maxAccess ? maxAccess : undefined,
        hideEmail: isSend ? hideEmail : undefined,
      });
      created = s;
      oncreated?.(s);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function copy(value: string, key: 'cloud.share.linkCopied' | 'cloud.share.passwordCopied') {
    try {
      await navigator.clipboard.writeText(value);
      app.toast(t(key));
    } catch {
      app.toast(t('compose.passwordCopyFailed'), undefined, 'error');
    }
  }
</script>

<Modal {title} {onclose} width="520px">
  {#if created}
    <div class="result">
      <p class="ok"><CircleCheck size={18} /> {t('cloud.share.created')}</p>
      <label>
        <span class="label">{t('cloud.share.link')}</span>
        <span class="copyrow">
          <input class="input" readonly value={created.url} />
          <button class="icon-btn" onclick={() => copy(created!.url, 'cloud.share.linkCopied')} aria-label={t('cloud.share.copyLink')} title={t('cloud.share.copyLink')}><Copy size={16} /></button>
        </span>
      </label>
      {#if created.password}
        <label>
          <span class="label">{t('cloud.share.password')}</span>
          <span class="copyrow">
            <input class="input mono" readonly value={created.password} />
            <button class="icon-btn" onclick={() => copy(created!.password!, 'cloud.share.passwordCopied')} aria-label={t('cloud.share.copyPassword')} title={t('cloud.share.copyPassword')}><Copy size={16} /></button>
          </span>
        </label>
        <p class="hint">{t('cloud.share.passwordSeparately')}</p>
      {/if}
      {#if created.expiresAt}<p class="hint">{t('cloud.share.validUntil', { date: formatFullDate(created.expiresAt) })}</p>{/if}
      <div class="row-actions"><button class="btn btn-primary" onclick={onclose} data-autofocus>{t('common.action.close')}</button></div>
    </div>
  {:else}
    <form
      class="form"
      onsubmit={(e) => {
        e.preventDefault();
        void create();
      }}
    >
      {#if isSend}
        <p class="hint">{t('cloud.share.sendHint')}</p>
        {#if !file}
          <label><span class="label">{t('cloud.share.text')}</span><textarea class="input" rows="4" bind:value={text} data-autofocus></textarea></label>
        {/if}
      {/if}
      <label class="check"><input type="checkbox" bind:checked={usePassword} /> {t('cloud.share.usePassword')}</label>
      {#if usePassword}
        <span class="copyrow">
          <input class="input mono" bind:value={password} placeholder={t('cloud.share.passwordAuto')} aria-label={t('cloud.share.password')} autocomplete="new-password" />
          <button type="button" class="btn" onclick={generate}><KeyRound size={15} /> {t('cloud.share.generate')}</button>
        </span>
      {/if}
      <label><span class="label">{t('cloud.share.expires')}</span><input class="input" type="date" bind:value={expires} min={minDate} required /></label>
      {#if isSend}
        <label>
          <span class="label">{t('cloud.share.maxAccess')}</span>
          <input class="input" type="number" min="1" max="1000" bind:value={maxAccess} placeholder={t('cloud.share.unlimited')} />
        </label>
        <label class="check"><input type="checkbox" bind:checked={hideEmail} /> {t('cloud.share.hideEmail')}</label>
      {:else}
        <label><span class="label">{t('cloud.share.note')}</span><input class="input" bind:value={note} placeholder={t('cloud.share.notePlaceholder')} /></label>
      {/if}
      {#if error}<p class="error" role="alert">{error}</p>{/if}
      <div class="row-actions">
        <button type="button" class="btn btn-ghost" onclick={onclose}>{t('common.action.cancel')}</button>
        <button type="submit" class="btn btn-primary" disabled={busy}>
          {#if busy}<LoaderCircle size={15} class="spin" />{/if}
          {isSend ? t('cloud.share.createSend') : t('cloud.share.create')}
        </button>
      </div>
    </form>
  {/if}
</Modal>

<style>
  .form,
  .result {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  label.check {
    flex-direction: row;
    align-items: center;
    gap: 8px;
    font-size: 13.5px;
  }
  .label {
    font-size: 12.5px;
    font-weight: 600;
    color: var(--tm-muted);
  }
  .copyrow {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .copyrow .input {
    flex: 1;
    min-width: 0;
  }
  .mono {
    font-family: var(--tm-font-mono, ui-monospace, monospace);
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
  .row-actions {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
  }
</style>
