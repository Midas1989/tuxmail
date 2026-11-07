<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  In the Android app: what the device token may not do is not offered as a button that
  fails, but as this hint with a link to the server (docs/security/BASIS-ABWEICHUNGEN.md).
-->
<script lang="ts">
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import { t } from '../../i18n/index.svelte';
  import { readActiveMirror } from '../../native/session';
  import { openExternal } from '../../platform';

  interface Props {
    /** What this hint is about. */
    what?: string;
  }
  let { what }: Props = $props();
  const server = readActiveMirror()?.serverUrl ?? '';
</script>

<p class="browser-only" role="note" data-testid="browser-only">
  {#if what}<strong>{what}:</strong>{/if}
  {t('errors.browserOnly')}
  {#if server}
    <button type="button" class="btn btn-ghost sm" onclick={() => openExternal(server)}><ExternalLink size={13} /> {t('errors.openServer')}</button>
  {/if}
</p>

<style>
  .browser-only {
    margin: 8px 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
</style>
