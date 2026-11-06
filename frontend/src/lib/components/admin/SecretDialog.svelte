<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Shows a secret exactly once: a generated password, an invite link or a
  backup key. The
  server never returns it again, so the dialog does not close by accident
  (Esc / click outside) before it was copied; "Done" always closes it. The
  value lives only in this component's props and is gone with it.
-->
<script lang="ts">
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import Modal from '../common/Modal.svelte';
  import { t } from '../../i18n/index.svelte';
  import { copyText } from '../../admin/clipboard';

  interface Props {
    title: string;
    /** What the value is, for the label of the field ("Password", "Invite link"). */
    label: string;
    /** Sentence above the field, e.g. who the password is for. */
    intro: string;
    value: string;
    /** If set, "Done" stays disabled until this statement is ticked ("Saved in the vault"). */
    ack?: string;
    onclose: () => void;
  }
  let { title, label, intro, value, ack, onclose }: Props = $props();

  let copied = $state(false);
  let failed = $state(false);
  let warn = $state(false);
  let acked = $state(false);
  let field = $state<HTMLInputElement>();

  async function copy() {
    failed = !(await copyText(value));
    copied = !failed;
    if (failed) field?.select();
  }

  /** Esc / backdrop: only once the secret was copied; otherwise point at the copy button. */
  function guardedClose() {
    if (ack ? acked : copied) onclose();
    else warn = true;
  }
</script>

<Modal {title} onclose={guardedClose} closable={false} width="520px">
  <p class="intro">{intro}</p>
  <label class="label" for="secret-field">{label}</label>
  <div class="row">
    <input id="secret-field" bind:this={field} class="input mono" readonly value={value} data-testid="secret-value" onfocus={(e) => e.currentTarget.select()} />
    <button class="btn" onclick={copy} data-autofocus data-testid="secret-copy">
      {#if copied}<Check size={14} /> {t('admin.secret.copied')}{:else}<Copy size={14} /> {t('admin.secret.copy')}{/if}
    </button>
  </div>
  <p class="once" role="note"><TriangleAlert size={14} aria-hidden="true" /> {t('admin.secret.once')}</p>
  {#if ack}
    <label class="ack"><input type="checkbox" bind:checked={acked} data-testid="secret-ack" /> {ack}</label>
  {/if}
  <div class="status" role="status">
    {#if failed}{t('admin.secret.copyFailed')}{:else if warn && ack}{t('admin.secret.ackFirst')}{:else if warn && !copied}{t('admin.secret.copyFirst')}{/if}
  </div>
  {#snippet footer()}
    <button class="btn btn-primary" onclick={onclose} disabled={!!ack && !acked} data-testid="secret-done">{t('admin.secret.done')}</button>
  {/snippet}
</Modal>

<style>
  .intro {
    margin: 0 0 12px;
    font-size: 14px;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .row .input {
    flex: 1 1 220px;
    min-width: 0;
  }
  .mono {
    font-family: var(--tm-mono);
    letter-spacing: 0.02em;
  }
  .once {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 12px 0 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .ack {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 12px;
    font-size: 14px;
    font-weight: 500;
  }
  .status {
    min-height: 18px;
    margin-top: 6px;
    font-size: 13px;
    color: var(--tm-danger);
  }
</style>
