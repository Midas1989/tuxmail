<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { t } from '../../i18n/index.svelte';
  import { tk } from '../../calendar/i18n';
  import type { RecurrenceScope } from '../../types';
  import Modal from '../common/Modal.svelte';

  interface Props {
    mode: 'edit' | 'delete';
    onchoose: (scope: RecurrenceScope) => void;
    onclose: () => void;
  }
  let { mode, onchoose, onclose }: Props = $props();
  let scope = $state<RecurrenceScope>('this');
</script>

<Modal title={mode === 'edit' ? t('calendar.scope.editTitle') : t('calendar.scope.deleteTitle')} {onclose} width="400px">
  <fieldset class="opts">
    <legend class="sr-only">{t('calendar.scope.legend')}</legend>
    {#each ['this', 'following', 'all'] as const as s (s)}
      <label><input type="radio" name="scope" value={s} bind:group={scope} data-autofocus={s === 'this' ? '' : undefined} /> {tk(`calendar.scope.${s}`)}</label>
    {/each}
  </fieldset>
  {#snippet footer()}
    <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary" class:danger={mode === 'delete'} onclick={() => onchoose(scope)}>{mode === 'edit' ? t('common.action.save') : t('common.action.delete')}</button>
  {/snippet}
</Modal>

<style>
  .opts {
    border: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  label {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .danger {
    background: var(--tm-danger);
    border-color: var(--tm-danger);
  }
</style>
