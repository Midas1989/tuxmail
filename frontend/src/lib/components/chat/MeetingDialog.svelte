<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- "Termin vorschlagen": free slots from the calendar as selectable chips, sent as a message (mail: with .ics). -->
<script lang="ts">
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import CalendarClock from '@lucide/svelte/icons/calendar-clock';
  import { app } from '../../state';
  import type { ChannelOption } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import type { TimeSlot } from '../../types';
  import Modal from '../common/Modal.svelte';

  interface Props {
    option: ChannelOption;
    onclose: () => void;
  }
  let { option, onclose }: Props = $props();

  const iso = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  const today = new Date();
  let title = $state(t('chat.meeting.defaultTitle', { name: app.conversation?.title ?? '' }));
  let duration = $state(30);
  let from = $state(iso(new Date(today.getTime() + 86400_000)));
  let to = $state(iso(new Date(today.getTime() + 7 * 86400_000)));
  let slots = $state<TimeSlot[]>([]);
  let chosen = $state<string[]>([]);
  let loading = $state(false);

  $effect(() => {
    const q = { from: new Date(from + 'T00:00').toISOString(), to: new Date(to + 'T23:59').toISOString(), durationMin: duration };
    loading = true;
    void app.api.findFreeSlots(q).then((s) => {
      slots = s;
      chosen = s.slice(0, 3).map((x) => x.start);
      loading = false;
    });
  });

  function toggle(start: string) {
    chosen = chosen.includes(start) ? chosen.filter((c) => c !== start) : chosen.length < 5 ? [...chosen, start] : chosen;
  }

  async function send() {
    const picked = slots.filter((s) => chosen.includes(s.start));
    if (!picked.length || !title.trim()) return;
    onclose();
    await app.proposeMeeting({ title: title.trim(), durationMin: duration, slots: picked }, option);
  }
</script>

<Modal title={t('chat.meeting.title')} {onclose} width="540px">
  <div class="form">
    <label><span class="label">{t('chat.meeting.subject')}</span><input class="input" bind:value={title} data-autofocus /></label>
    <div class="row">
      <label>
        <span class="label">{t('chat.meeting.duration')}</span>
        <select class="input" bind:value={duration}>
          {#each [15, 30, 45, 60, 90] as m (m)}<option value={m}>{t('chat.meeting.minutes', { count: m })}</option>{/each}
        </select>
      </label>
      <label><span class="label">{t('search.dateFrom')}</span><input class="input" type="date" bind:value={from} /></label>
      <label><span class="label">{t('search.dateTo')}</span><input class="input" type="date" bind:value={to} /></label>
    </div>
    <div class="slots-head">
      <span class="label">{t('chat.meeting.freeSlots')}</span>
      {#if loading}<LoaderCircle size={14} class="spin" />{/if}
      <small>{t('chat.meeting.pickUpTo')}</small>
    </div>
    <div class="slots" role="group" aria-label={t('chat.meeting.freeSlots')}>
      {#each slots as s (s.start)}
        <button class="slot" aria-pressed={chosen.includes(s.start)} onclick={() => toggle(s.start)}>{fmt().full(s.start)}</button>
      {:else}
        {#if !loading}<p class="muted">{t('chat.meeting.noSlots')}</p>{/if}
      {/each}
    </div>
    <p class="hint">{option.channel === 'email' ? t('chat.meeting.hintMail') : t('chat.meeting.hintChat')}</p>
  </div>
  {#snippet footer()}
    <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary" disabled={!chosen.length || !title.trim()} onclick={send}><CalendarClock size={15} /> {t('chat.meeting.send', { count: chosen.length })}</button>
  {/snippet}
</Modal>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .row {
    display: grid;
    grid-template-columns: 1fr 1fr 1fr;
    gap: 8px;
  }
  .slots-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .slots-head small {
    margin-left: auto;
    color: var(--tm-faint);
    font-size: 11.5px;
  }
  .slots {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .slot {
    padding: 6px 12px;
    border-radius: 99px;
    border: 1px solid var(--tm-line-strong);
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    font-size: 12.5px;
  }
  .slot[aria-pressed='true'] {
    background: var(--tm-accent-soft);
    border-color: var(--tm-accent);
    color: var(--tm-accent-text);
    font-weight: 650;
  }
  .hint,
  .muted {
    margin: 0;
    font-size: 12px;
    color: var(--tm-muted);
  }
  @media (max-width: 639px) {
    .row {
      grid-template-columns: 1fr;
    }
  }
</style>
