<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings -> Administration -> Backup: the age key of the server backups. Shows
  whether there is a key and how the last run went, and creates, replaces or
  imports a key after a fresh confirmation. The private key is held only in the
  `secret` variable while its dialog is open; the server does not keep it.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import Modal from '../common/Modal.svelte';
  import StepUpForm from '../security/StepUpForm.svelte';
  import SecretDialog from './SecretDialog.svelte';
  import { t, fmt } from '../../i18n/index.svelte';
  import type { BackupRun, BackupStatus } from '../../api/admin';
  import type { Reauth } from '../../api/security';
  import type { AdminPanelProps } from '../../admin/panel';
  import { adminErrorMessage } from '../../admin/errors';

  let { client, notify }: AdminPanelProps = $props();

  let status = $state<BackupStatus | null>(null);
  let loadError = $state('');
  /** The action waiting for its confirmation. */
  let pending = $state<{ kind: 'generate' } | { kind: 'import'; publicKey: string } | null>(null);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let secret = $state<string | null>(null);
  let ownOpen = $state(false);
  let ownKey = $state('');

  const hasKey = $derived((status?.keys.length ?? 0) > 0);
  const replacing = $derived(hasKey || !!status?.invalid);

  async function load() {
    try {
      status = await client.backupStatus();
      loadError = '';
    } catch (err) {
      loadError = adminErrorMessage(err, t);
    }
  }
  onMount(load);

  function runText(run: BackupRun): string {
    const date = fmt().dateTime(run.finishedAt);
    if (run.result === 'failed') return t('admin.backup.runFailed', { date });
    if (run.result !== 'ok') return t('admin.backup.runNoKey', { date });
    return t(run.encrypted ? 'admin.backup.runOk' : 'admin.backup.runPlain', { date });
  }
  const runBad = (run: BackupRun) => run.result !== 'ok' || !run.encrypted;

  function ask(action: NonNullable<typeof pending>) {
    error = null;
    pending = action;
  }

  async function confirm(reauth: Reauth) {
    if (!pending) return;
    busy = true;
    error = null;
    try {
      if (pending.kind === 'generate') {
        secret = (await client.generateBackupKey(reauth, replacing)).privateKey;
      } else {
        await client.importBackupKey(pending.publicKey, reauth, replacing);
        notify(t('admin.toast.backupKeyImported'));
        ownKey = '';
        ownOpen = false;
      }
      pending = null;
      await load();
    } catch (err) {
      error = adminErrorMessage(err, t);
    } finally {
      busy = false;
    }
  }

  function submitOwn(e: SubmitEvent) {
    e.preventDefault();
    if (ownKey.trim()) ask({ kind: 'import', publicKey: ownKey.trim() });
  }
</script>

<div class="backup">
  <p class="what">{t('admin.backup.what')}</p>

  {#if loadError}
    <p class="alert" role="alert">{loadError}</p>
  {:else if !status}
    <p class="what">{t('admin.loading')}</p>
  {:else}
    {#if status.invalid}
      <p class="alert" role="alert" data-testid="backup-invalid"><TriangleAlert size={16} aria-hidden="true" /> {t('admin.backup.invalid')}</p>
    {:else if !hasKey}
      <p class="alert" role="alert" data-testid="backup-nokey"><TriangleAlert size={16} aria-hidden="true" /> {t('admin.backup.noKey')}</p>
    {/if}

    {#each status.keys as key (key.publicKey)}
      <dl class="facts" data-testid="backup-key">
        <dt>{t('admin.backup.fingerprint')}</dt>
        <dd class="mono">{key.fingerprint}</dd>
        <dt>{t('admin.backup.publicKey')}</dt>
        <dd class="mono">{key.publicKey}</dd>
      </dl>
    {/each}

    <dl class="facts">
      <dt>{t('admin.backup.lastRun')}</dt>
      {#if status.lastRun}
        <dd class:bad={runBad(status.lastRun)} data-testid="backup-run">{runText(status.lastRun)}</dd>
      {:else}
        <dd data-testid="backup-run">{t('admin.backup.neverRun')}</dd>
      {/if}
    </dl>

    <div class="actions">
      <button class="btn" class:btn-primary={!replacing} onclick={() => ask({ kind: 'generate' })} data-testid="backup-generate">
        <KeyRound size={15} />
        {replacing ? t('admin.backup.replace') : t('admin.backup.generate')}
      </button>
      <button class="btn btn-ghost" aria-expanded={ownOpen} onclick={() => (ownOpen = !ownOpen)} data-testid="backup-own-toggle">{t('admin.backup.useOwn')}</button>
    </div>

    {#if ownOpen}
      <form class="own" onsubmit={submitOwn}>
        <label class="label" for="backup-own">{t('admin.backup.ownLabel')}</label>
        <input id="backup-own" class="input mono" bind:value={ownKey} autocomplete="off" autocapitalize="off" spellcheck="false" data-testid="backup-own" />
        <p class="hint">{t('admin.backup.ownHint')}</p>
        <div><button class="btn" disabled={!ownKey.trim()} data-testid="backup-own-submit">{t('admin.backup.continue')}</button></div>
      </form>
    {/if}
  {/if}
</div>

{#if pending && status}
  {@const title = pending.kind === 'import' ? t('admin.backup.confirmImport') : replacing ? t('admin.backup.confirmReplace') : t('admin.backup.confirmGenerate')}
  <Modal {title} onclose={() => (pending = null)}>
    {#if replacing}
      <p class="alert" role="alert" data-testid="backup-replace-warning"><TriangleAlert size={16} aria-hidden="true" /> {t('admin.backup.replaceWarning')}</p>
    {/if}
    <StepUpForm hasPassword={status.reauth.password} hasSecondFactor={status.reauth.code} submitLabel={title} danger={replacing} {busy} {error} onsubmit={(r) => void confirm(r)} oncancel={() => (pending = null)} />
  </Modal>
{/if}

{#if secret}
  <SecretDialog title={t('admin.backup.secretTitle')} label={t('admin.backup.secretLabel')} intro={t('admin.backup.secretIntro')} value={secret} ack={t('admin.backup.secretAck')} onclose={() => (secret = null)} />
{/if}

<style>
  .backup {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .what {
    margin: 0;
    font-size: 14px;
  }
  .alert {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    margin: 0 0 4px;
    padding: 10px 12px;
    border-radius: var(--tm-radius-sm);
    background: color-mix(in srgb, var(--tm-danger) 12%, transparent);
    font-size: 14px;
    font-weight: 500;
  }
  .alert :global(svg) {
    flex: none;
    margin-top: 2px;
    color: var(--tm-danger);
  }
  .facts {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin: 0;
  }
  dt {
    font-size: 12px;
    color: var(--tm-muted);
  }
  dd {
    margin: 0 0 6px;
    font-size: 14px;
  }
  dd.bad {
    color: var(--tm-danger);
    font-weight: 500;
  }
  .mono {
    font-family: var(--tm-mono);
    font-size: 13px;
    overflow-wrap: anywhere;
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .own {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-width: 640px;
  }
  .hint {
    margin: 0;
    font-size: 12px;
    color: var(--tm-faint);
  }
</style>
