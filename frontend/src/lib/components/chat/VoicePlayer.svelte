<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Voice note player with waveform. Plays `url` when present; demo data without audio simulates playback. -->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import Play from '@lucide/svelte/icons/play';
  import Pause from '@lucide/svelte/icons/pause';
  import type { Attachment } from '../../types';
  import { t } from '../../i18n/index.svelte';

  interface Props {
    attachment: Attachment;
    own?: boolean;
  }
  let { attachment: a, own = false }: Props = $props();

  let playing = $state(false);
  let pos = $state(0); // seconds
  let audio = $state<HTMLAudioElement>();
  let raf = 0;
  let last = 0;
  const duration = $derived(a.durationSec ?? 0);
  const bars = $derived(a.waveform?.length ? a.waveform : Array.from({ length: 36 }, (_, i) => 0.3 + 0.6 * Math.abs(Math.sin(i * 1.7))));

  function fmt(sec: number): string {
    const s = Math.max(0, Math.round(sec));
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
  }

  function tick(now: number) {
    if (!playing) return;
    pos = Math.min(duration, pos + (now - last) / 1000);
    last = now;
    if (pos >= duration) {
      playing = false;
      pos = 0;
      return;
    }
    raf = requestAnimationFrame(tick);
  }

  function toggle() {
    if (audio && a.url) {
      if (playing) audio.pause();
      else void audio.play();
      return;
    }
    playing = !playing;
    if (playing) {
      last = performance.now();
      raf = requestAnimationFrame(tick);
    } else cancelAnimationFrame(raf);
  }

  onDestroy(() => cancelAnimationFrame(raf));
</script>

<div class="voice" class:own role="group" aria-label={t('chat.media.voiceNote', { duration: fmt(duration) })}>
  <button class="play" onclick={toggle} aria-label={playing ? t('chat.media.pause') : t('chat.media.play')}>
    {#if playing}<Pause size={18} />{:else}<Play size={18} />{/if}
  </button>
  <div class="wave" aria-hidden="true">
    {#each bars as h, i (i)}
      <span class="bar" class:done={duration > 0 && i / bars.length < pos / duration} style:height="{Math.round(h * 100)}%"></span>
    {/each}
  </div>
  <span class="dur">{fmt(playing || pos > 0 ? pos : duration)}</span>
  {#if a.url}
    <audio
      bind:this={audio}
      src={a.url}
      preload="metadata"
      onplay={() => (playing = true)}
      onpause={() => (playing = false)}
      ontimeupdate={(e) => (pos = (e.currentTarget as HTMLAudioElement).currentTime)}
      onended={() => {
        playing = false;
        pos = 0;
      }}
    ></audio>
  {/if}
</div>

<style>
  .voice {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 220px;
    padding: 2px 0;
  }
  .play {
    flex: none;
    width: 36px;
    height: 36px;
    border-radius: 99px;
    border: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .own .play {
    background: rgb(255 255 255 / 0.9);
    color: var(--tm-brand-navy);
  }
  .wave {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 2px;
    height: 28px;
  }
  .bar {
    flex: 1;
    min-height: 3px;
    border-radius: 2px;
    background: currentColor;
    opacity: 0.35;
  }
  .bar.done {
    opacity: 0.95;
  }
  .dur {
    font-size: 11.5px;
    font-variant-numeric: tabular-nums;
    opacity: 0.8;
  }
</style>
