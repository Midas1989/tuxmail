<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { hueFor, initials } from '../../conversation';
  import UsersRound from '@lucide/svelte/icons/users-round';

  interface Props {
    name: string;
    seed?: string | number;
    size?: number;
    group?: boolean;
    url?: string;
  }
  let { name, seed, size = 36, group = false, url }: Props = $props();
  const hue = $derived(hueFor(seed ?? name));
</script>

<span
  class="avatar"
  style:width="{size}px"
  style:height="{size}px"
  style:font-size="{Math.round(size * 0.38)}px"
  style:--h={hue}
  aria-hidden="true"
>
  {#if url}
    <img src={url} alt="" />
  {:else if group}
    <UsersRound size={Math.round(size * 0.5)} />
  {:else}
    {initials(name)}
  {/if}
</span>

<style>
  .avatar {
    display: inline-flex;
    flex: none;
    align-items: center;
    justify-content: center;
    border-radius: 999px;
    font-weight: 600;
    letter-spacing: 0.02em;
    color: hsl(var(--h) 55% 32%);
    background: hsl(var(--h) 70% 88%);
    overflow: hidden;
    user-select: none;
  }
  :global([data-theme='dark']) .avatar {
    color: hsl(var(--h) 70% 82%);
    background: hsl(var(--h) 35% 26%);
  }
  img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
</style>
