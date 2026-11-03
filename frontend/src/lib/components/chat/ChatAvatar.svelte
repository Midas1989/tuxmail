<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Avatar (photo or initials) with a small channel badge in the corner. -->
<script lang="ts">
  import Mail from '@lucide/svelte/icons/mail';
  import MessageCircle from '@lucide/svelte/icons/message-circle';
  import Shield from '@lucide/svelte/icons/shield';
  import Send from '@lucide/svelte/icons/send';
  import type { Channel } from '../../types';
  import Avatar from '../common/Avatar.svelte';

  interface Props {
    name: string;
    seed?: string | number;
    size?: number;
    group?: boolean;
    url?: string;
    channel?: Channel;
  }
  let { name, seed, size = 46, group = false, url, channel }: Props = $props();
  const b = $derived(Math.max(14, Math.round(size * 0.38)));
</script>

<span class="wrap" style:width="{size}px" style:height="{size}px">
  <Avatar {name} {seed} {size} {group} {url} />
  {#if channel}
    <span class="badge ch-{channel}" style:width="{b}px" style:height="{b}px" aria-hidden="true">
      {#if channel === 'email'}<Mail size={b - 6} strokeWidth={2.4} />{:else if channel === 'whatsapp'}<MessageCircle size={b - 6} strokeWidth={2.4} />{:else if channel === 'signal'}<Shield size={b - 6} strokeWidth={2.4} />{:else}<Send size={b - 6} strokeWidth={2.4} />{/if}
    </span>
  {/if}
</span>

<style>
  .wrap {
    position: relative;
    display: inline-flex;
    flex: none;
  }
  .badge {
    position: absolute;
    right: -2px;
    bottom: -2px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border-radius: 99px;
    color: #fff;
    border: 2px solid var(--tm-surface);
  }
  .ch-email {
    background: var(--tm-mail);
  }
  .ch-whatsapp {
    background: var(--tm-whatsapp);
  }
  .ch-signal {
    background: var(--tm-signal);
  }
  .ch-telegram {
    background: var(--tm-telegram, #229ed9);
  }
</style>
