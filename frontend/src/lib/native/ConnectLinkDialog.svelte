<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Copyright (C) 2026 Midas Wollinger and TuxMail contributors -->
<!--
  Confirmation for tuxmail://connect?server=... (NG-01). Names the FULL host
  of the link and the host of the current session. Cancel is the default action.
-->
<script lang="ts">
  import Modal from '../components/common/Modal.svelte';
  import { t } from '../i18n/index.svelte';

  interface Props {
    /** Full host of the link, e.g. "mail.example.org:8443". */
    host: string;
    /** Host of the active session; null when signed out. */
    current: string | null;
    onconfirm: () => void;
    oncancel: () => void;
  }
  let { host, current, onconfirm, oncancel }: Props = $props();
</script>

<Modal title={t('native.connectLink.title', { server: host })} onclose={oncancel} width="420px">
  <p class="host" data-testid="connect-link-host">{host}</p>
  <p class="msg">{current ? t('native.connectLink.messageSignedIn', { server: host, current }) : t('native.connectLink.message', { server: host })}</p>
  <p class="warn" role="note">{t('native.connectLink.warning')}</p>
  {#snippet footer()}
    <button class="btn" onclick={oncancel} data-autofocus>{t('common.action.cancel')}</button>
    <button class="btn" onclick={onconfirm}>{t('native.connectLink.continue')}</button>
  {/snippet}
</Modal>

<style>
  .host {
    margin: 0 0 10px;
    font-family: var(--tm-mono);
    font-size: 15px;
    font-weight: 600;
    word-break: break-all;
    user-select: all;
  }
  .msg {
    margin: 0 0 8px;
    font-size: 14px;
    color: var(--tm-fg);
  }
  .warn {
    margin: 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
</style>
