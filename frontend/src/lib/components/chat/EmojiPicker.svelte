<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Small dependency-free emoji picker (common emoji by category). -->
<script lang="ts" module>
  export const QUICK_REACTIONS = ['👍', '❤️', '😂', '😮', '😢', '🙏'];

  export const EMOJI: { id: string; icon: string; list: string[] }[] = [
    { id: 'smileys', icon: '😀', list: [...'😀😃😄😁😆😅😂🤣🙂😉😊😇🥰😍🤩😘😗😚😋😛😜🤪😝🤗🤭🤫🤔🤐😐😑😶🙄😏😬😌😔😪😴😷🤒🤕🥵🥶😎🤓🧐😕😟🙁😮😯😲😳🥺😢😭😱😖😣😞😓😩😫🥱😤😡🤯🥳'].filter((c) => c.trim()) },
    { id: 'gestures', icon: '👍', list: ['👍', '👎', '👌', '✌️', '🤞', '🤟', '🤘', '🤙', '👋', '👏', '🙌', '🙏', '💪', '👀', '🤝', '✋', '🫶', '☝️', '👉', '👈'] },
    { id: 'hearts', icon: '❤️', list: ['❤️', '🧡', '💛', '💚', '💙', '💜', '🖤', '🤍', '💔', '❣️', '💕', '💞', '💖', '✨', '⭐', '🔥', '💯', '✅', '❌', '❓'] },
    { id: 'nature', icon: '🌿', list: ['🌞', '🌧️', '⛅', '❄️', '🌈', '🌊', '⛰️', '🏔️', '🌲', '🌻', '🌹', '🍀', '🐧', '🐶', '🐱', '🐻', '🦊', '🐝', '🦋', '🐢'] },
    { id: 'food', icon: '🍕', list: ['☕', '🍺', '🍷', '🥂', '🍕', '🍔', '🥨', '🥗', '🍝', '🍰', '🎂', '🍎', '🍓', '🍫', '🍦', '🥐', '🍳', '🍜', '🌮', '🍉'] },
    { id: 'objects', icon: '📎', list: ['📎', '📅', '📌', '📞', '💻', '📱', '✉️', '📦', '🔑', '💡', '🎉', '🎁', '🏠', '🚗', '🚆', '✈️', '⏰', '📷', '🎵', '⚽'] },
  ];
</script>

<script lang="ts">
  import { t } from '../../i18n/index.svelte';

  interface Props {
    onpick: (emoji: string) => void;
    onclose: () => void;
  }
  let { onpick, onclose }: Props = $props();
  let cat = $state(EMOJI[0].id);
  let grid = $state<HTMLDivElement>();
  const current = $derived(EMOJI.find((c) => c.id === cat) ?? EMOJI[0]);

  $effect(() => {
    grid?.querySelector<HTMLElement>('button')?.focus();
  });

  function onkeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation();
      onclose();
      return;
    }
    if (!grid) return;
    const btns = [...grid.querySelectorAll<HTMLButtonElement>('button')];
    const idx = btns.indexOf(document.activeElement as HTMLButtonElement);
    if (idx < 0) return;
    const cols = 8;
    const step = { ArrowRight: 1, ArrowLeft: -1, ArrowDown: cols, ArrowUp: -cols }[e.key];
    if (step) {
      e.preventDefault();
      btns[Math.max(0, Math.min(btns.length - 1, idx + step))]?.focus();
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="backdrop" onmousedown={onclose} role="presentation"></div>
<div class="picker" role="dialog" aria-label={t('chat.emoji.title')} tabindex="-1" {onkeydown}>
  <div class="tabs" role="tablist" aria-label={t('chat.emoji.title')}>
    {#each EMOJI as c (c.id)}
      <button role="tab" aria-selected={c.id === cat} title={t(`chat.emoji.${c.id}` as 'chat.emoji.smileys')} aria-label={t(`chat.emoji.${c.id}` as 'chat.emoji.smileys')} onclick={() => (cat = c.id)}>{c.icon}</button>
    {/each}
  </div>
  <div class="grid" bind:this={grid} role="group" aria-label={t(`chat.emoji.${current.id}` as 'chat.emoji.smileys')}>
    {#each current.list as e (e)}
      <button onclick={() => onpick(e)} aria-label={e}>{e}</button>
    {/each}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 60;
  }
  .picker {
    position: absolute;
    bottom: calc(100% + 8px);
    left: 8px;
    z-index: 61;
    width: 320px;
    padding: 8px;
    border-radius: 14px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
  }
  .tabs {
    display: flex;
    gap: 2px;
    border-bottom: 1px solid var(--tm-line);
    padding-bottom: 6px;
    margin-bottom: 6px;
  }
  .tabs button {
    flex: 1;
    height: 30px;
    border: none;
    border-radius: 8px;
    background: transparent;
    font-size: 17px;
    opacity: 0.6;
  }
  .tabs button[aria-selected='true'] {
    background: var(--tm-surface-2);
    opacity: 1;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(8, 1fr);
    gap: 2px;
    max-height: 210px;
    overflow-y: auto;
  }
  .grid button {
    height: 36px;
    border: none;
    border-radius: 8px;
    background: transparent;
    font-size: 22px;
    line-height: 1;
  }
  .grid button:hover,
  .grid button:focus-visible {
    background: var(--tm-surface-2);
    outline: none;
  }
</style>
