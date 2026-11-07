<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- The choice "Über den Server" / "Auf diesem Gerät", in plain words. -->
<script lang="ts">
  import { t } from '../../i18n/index.svelte';
  import type { ClientMode } from '../../types';

  interface Props {
    mode: ClientMode;
    /** Accounts of the server use POP3: fetching on two sides can lose mail. */
    pop3?: boolean;
  }
  let { mode = $bindable(), pop3 = false }: Props = $props();
</script>

<fieldset class="modes" data-testid="mode-choice">
  <legend>{t('onboarding.connect.mode')}</legend>
  <label class="mode">
    <input type="radio" name="client-mode" value="thin" bind:group={mode} />
    <span class="body">
      <b>{t('onboarding.connect.modeThin')} <em class="rec">{t('connection.mode.recommended')}</em></b>
      <ul>
        <li>{t('connection.mode.thin1')}</li>
        <li>{t('connection.mode.thin2')}</li>
        <li>{t('connection.mode.thin3')}</li>
        <li>{t('connection.mode.thin4')}</li>
      </ul>
    </span>
  </label>
  <label class="mode">
    <input type="radio" name="client-mode" value="local" bind:group={mode} />
    <span class="body">
      <b>{t('onboarding.connect.modeLocal')}</b>
      <ul>
        <li>{t('connection.mode.local1')}</li>
        <li>{t('connection.mode.local2')}</li>
        <li>{t('connection.mode.local3')}</li>
        <li>{t('connection.mode.local4')}</li>
      </ul>
    </span>
  </label>
  <p class="both">{t('connection.mode.both')}</p>
  {#if mode === 'local' && pop3}
    <p class="warn" role="alert" data-testid="pop3-warning">{t('connection.mode.pop3')}</p>
  {/if}
  {#if mode === 'thin'}
    <p class="note">{t('connection.mode.thinLocalAccounts')}</p>
    <p class="note">{t('connection.mode.thinCache')}</p>
  {/if}
</fieldset>

<style>
  .modes {
    display: flex;
    flex-direction: column;
    gap: 8px;
    border: 0;
    padding: 0;
    margin: 0;
  }
  legend {
    font-size: 12.5px;
    font-weight: 600;
    padding: 0 0 4px;
  }
  .mode {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px 12px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    cursor: pointer;
  }
  .mode:has(input:checked) {
    border-color: var(--tm-accent);
    background: var(--tm-accent-soft);
  }
  .mode input {
    margin-top: 3px;
    accent-color: var(--tm-accent);
  }
  .body {
    font-size: 13.5px;
  }
  ul {
    list-style: disc;
    margin: 4px 0 0;
    padding-left: 16px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .rec {
    margin-left: 6px;
    padding: 1px 8px;
    border-radius: 99px;
    background: var(--tm-accent);
    color: var(--tm-accent-fg, #fff);
    font-size: 11px;
    font-style: normal;
    font-weight: 600;
  }
  .both,
  .note {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .warn {
    margin: 0;
    padding: 8px 10px;
    border-radius: 10px;
    font-size: 12.5px;
    background: color-mix(in srgb, var(--tm-warn) 16%, transparent);
    border: 1px solid var(--tm-warn);
  }
</style>
