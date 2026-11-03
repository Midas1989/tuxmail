<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Modal from './Modal.svelte';
  import { t } from '../../i18n/index.svelte';

  interface Props {
    title: string;
    message: string;
    confirmLabel: string;
    danger?: boolean;
    onconfirm: () => void;
    oncancel: () => void;
  }
  let { title, message, confirmLabel, danger = false, onconfirm, oncancel }: Props = $props();
</script>

<Modal {title} onclose={oncancel} width="420px">
  <p class="msg">{message}</p>
  {#snippet footer()}
    <button class="btn" onclick={oncancel}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary" class:danger onclick={onconfirm} data-autofocus>{confirmLabel}</button>
  {/snippet}
</Modal>

<style>
  .msg {
    margin: 0;
    font-size: 14px;
    color: var(--tm-fg);
  }
  .danger {
    background: var(--tm-danger);
    border-color: var(--tm-danger);
    color: #fff;
  }
</style>
