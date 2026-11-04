<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Mail from '@lucide/svelte/icons/mail';
  import MessageCircle from '@lucide/svelte/icons/message-circle';
  import Shield from '@lucide/svelte/icons/shield';
  import Send from '@lucide/svelte/icons/send';
  import Hash from '@lucide/svelte/icons/hash';
  import type { Channel } from '../../types';
  import { t } from '../../i18n/index.svelte';

  interface Props {
    channel: Channel;
    size?: number;
    /** Render as a filled round badge instead of a bare glyph. */
    badge?: boolean;
    label?: boolean;
  }
  let { channel, size = 14, badge = false, label = false }: Props = $props();
  const name = $derived(t(`common.channel.${channel}`));
</script>

<span class="ch ch-{channel}" class:badge title={name}>
  {#if channel === 'email'}
    <Mail size={badge ? size - 4 : size} aria-hidden="true" />
  {:else if channel === 'whatsapp'}
    <MessageCircle size={badge ? size - 4 : size} aria-hidden="true" />
  {:else if channel === 'telegram'}
    <Send size={badge ? size - 4 : size} aria-hidden="true" />
  {:else if channel === 'matrix'}
    <Hash size={badge ? size - 4 : size} aria-hidden="true" />
  {:else}
    <Shield size={badge ? size - 4 : size} aria-hidden="true" />
  {/if}
  {#if label}<span class="lbl">{name}</span>{:else}<span class="sr-only">{name}</span>{/if}
</span>

<style>
  .ch {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex: none;
  }
  .ch-email {
    color: var(--tm-mail);
  }
  .ch-whatsapp {
    color: var(--tm-whatsapp);
  }
  .ch-signal {
    color: var(--tm-signal);
  }
  .ch-telegram {
    color: var(--tm-telegram);
  }
  .badge.ch-telegram {
    background: var(--tm-telegram);
  }
  .badge {
    border-radius: 999px;
    padding: 2px;
    color: white;
  }
  .badge.ch-email {
    background: var(--tm-mail);
  }
  .badge.ch-whatsapp {
    background: var(--tm-whatsapp);
  }
  .badge.ch-signal {
    background: var(--tm-signal);
  }
  .ch-matrix {
    color: var(--tm-matrix, #0dbd8b);
  }
  .badge.ch-matrix {
    background: var(--tm-matrix, #0dbd8b);
    color: white;
  }
  .lbl {
    font-size: 11.5px;
    font-weight: 500;
  }
</style>
