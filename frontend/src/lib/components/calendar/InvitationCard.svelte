<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import CalendarDays from '@lucide/svelte/icons/calendar-days';
  import Check from '@lucide/svelte/icons/check';
  import CircleHelp from '@lucide/svelte/icons/circle-help';
  import X from '@lucide/svelte/icons/x';
  import Clock from '@lucide/svelte/icons/clock';
  import MapPin from '@lucide/svelte/icons/map-pin';
  import Video from '@lucide/svelte/icons/video';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import { app } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import { tk } from '../../calendar/i18n';
  import type { Invitation, InvitationResponse } from '../../types';
  import { safeHref } from '../../safeurl';

  interface Props {
    invitation: Invitation;
    compact?: boolean;
    onchange?: (inv: Invitation) => void;
  }
  let { invitation, compact = false, onchange }: Props = $props();

  let answered = $state<Invitation | null>(null);
  const inv = $derived(answered && answered.id === invitation.id ? answered : invitation);
  let busy = $state(false);
  let showComment = $state(false);
  let comment = $state('');
  let proposing = $state(false);
  let pStart = $state('');
  let pEnd = $state('');

  const when = $derived.by(() => {
    if (inv.allDay) return fmt().dateLong(inv.start + 'T12:00:00Z');
    const sameDay = fmt().dayKey(inv.start) === fmt().dayKey(new Date(new Date(inv.end).getTime() - 1));
    return sameDay ? `${fmt().dateLong(inv.start)}, ${fmt().time(inv.start)} – ${fmt().time(inv.end)}` : `${fmt().dateTime(inv.start)} – ${fmt().dateTime(inv.end)}`;
  });
  const organizer = $derived(inv.organizer?.name || inv.organizer?.email || inv.sender || '');

  function local(iso: string): string {
    const d = new Date(iso);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  }

  async function respond(partStat: InvitationResponse['partStat'], extra: Partial<InvitationResponse> = {}) {
    busy = true;
    try {
      answered = await app.api.respondInvitation(inv.id, { partStat, comment: comment || undefined, ...extra });
      onchange?.(answered);
      app.toast(extra.proposeStart ? t('calendar.invite.proposalSent') : tk(`calendar.invite.sent.${partStat}`));
      showComment = false;
      proposing = false;
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    } finally {
      busy = false;
    }
  }

  function startPropose() {
    pStart = local(inv.start);
    pEnd = local(inv.end);
    proposing = true;
  }

  function sendProposal() {
    void respond('TENTATIVE', { proposeStart: new Date(pStart).toISOString(), proposeEnd: new Date(pEnd).toISOString() });
  }

  const statusText = $derived.by(() => {
    switch (inv.method) {
      case 'REPLY':
        return t('calendar.invite.reply', { name: inv.sender ?? '', status: tk(`calendar.partstat.${inv.replyPartStat ?? 'NEEDS-ACTION'}`) });
      case 'CANCEL':
        return t('calendar.invite.status.cancelled');
      case 'COUNTER':
        return t('calendar.invite.counter', { name: inv.sender ?? '' });
    }
    if (inv.status === 'obsolete') return t('calendar.invite.status.obsolete');
    if (inv.status === 'cancelled') return t('calendar.invite.status.cancelled');
    if (inv.myPartStat && inv.myPartStat !== 'NEEDS-ACTION') return tk(`calendar.invite.status.${inv.myPartStat}`);
    return '';
  });
</script>

<section class="inv" class:compact class:cancelled={inv.method === 'CANCEL' || inv.status === 'cancelled'} aria-label={t('calendar.invite.title')}>
  <div class="head">
    <span class="ic"><CalendarDays size={18} /></span>
    <div class="ttl">
      <span class="kind">{tk(`calendar.invite.method.${inv.method}`)}</span>
      <strong>{inv.title}</strong>
    </div>
  </div>
  <ul class="facts">
    <li><Clock size={14} /> {when}{#if inv.recurring}&nbsp;· {t('calendar.invite.recurring')}{/if}</li>
    {#if inv.location}<li><MapPin size={14} /> {inv.location}</li>{/if}
    {#if safeHref(inv.meetingUrl)}<li><Video size={14} /> <a href={safeHref(inv.meetingUrl)} target="_blank" rel="noopener noreferrer">{t('calendar.event.join')}</a></li>{/if}
    {#if inv.organizerMismatch}<li class="warn" data-testid="invite-spoof"><TriangleAlert size={14} /> {t('calendar.invite.spoofWarning', { sender: inv.sender ?? '' })}</li>{/if}
    {#if organizer}<li class="muted">{t('calendar.invite.from', { name: organizer })} · {t('calendar.invite.attendees', { n: inv.attendees.length })}</li>{/if}
    {#if inv.conflicts.length}<li class="warn"><TriangleAlert size={14} /> {t('calendar.invite.conflicts', { list: inv.conflicts.join(', ') })}</li>{/if}
  </ul>
  {#if inv.comment}<p class="comment">{inv.comment}</p>{/if}
  {#if statusText}<p class="status" data-status={inv.myPartStat ?? inv.status}>{statusText}</p>{/if}
  {#if inv.canRespond && inv.method === 'COUNTER'}
    <div class="actions">
      <button class="btn btn-primary" disabled={busy} onclick={() => respond('ACCEPTED')}><Check size={15} /> {t('calendar.invite.acceptProposal')}</button>
      <button class="btn" disabled={busy} onclick={() => respond('DECLINED')}><X size={15} /> {t('calendar.invite.declineProposal')}</button>
    </div>
  {:else if inv.canRespond}
    <div class="actions" role="group" aria-label={t('calendar.invite.respond')}>
      <button class="btn" class:on={inv.myPartStat === 'ACCEPTED'} disabled={busy} onclick={() => respond('ACCEPTED')}><Check size={15} /> {t('calendar.invite.accept')}</button>
      <button class="btn" class:on={inv.myPartStat === 'TENTATIVE'} disabled={busy} onclick={() => respond('TENTATIVE')}><CircleHelp size={15} /> {t('calendar.invite.tentative')}</button>
      <button class="btn" class:on={inv.myPartStat === 'DECLINED'} disabled={busy} onclick={() => respond('DECLINED')}><X size={15} /> {t('calendar.invite.decline')}</button>
      {#if !compact}
        <button class="btn btn-ghost" onclick={() => (showComment = !showComment)}>{t('calendar.invite.addComment')}</button>
        <button class="btn btn-ghost" onclick={startPropose}>{t('calendar.invite.propose')}</button>
      {/if}
    </div>
    {#if showComment}
      <textarea class="input" rows="2" bind:value={comment} placeholder={t('calendar.invite.comment')} aria-label={t('calendar.invite.comment')}></textarea>
    {/if}
    {#if proposing}
      <div class="propose">
        <label><span class="label">{t('calendar.event.start')}</span><input class="input" type="datetime-local" bind:value={pStart} /></label>
        <label><span class="label">{t('calendar.event.end')}</span><input class="input" type="datetime-local" bind:value={pEnd} /></label>
        <button class="btn btn-primary" disabled={busy || !pStart || !pEnd} onclick={sendProposal}>{t('calendar.invite.proposeSend')}</button>
      </div>
    {/if}
  {/if}
</section>

<style>
  .inv {
    border: 1px solid var(--tm-line);
    border-left: 4px solid var(--tm-accent);
    border-radius: var(--tm-radius);
    background: var(--tm-surface);
    padding: 12px 14px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    max-width: 560px;
  }
  .inv.cancelled {
    border-left-color: var(--tm-danger);
  }
  .inv.cancelled strong {
    text-decoration: line-through;
  }
  .head {
    display: flex;
    gap: 10px;
    align-items: center;
  }
  .ic {
    width: 34px;
    height: 34px;
    border-radius: 9px;
    display: grid;
    place-items: center;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
    flex: none;
  }
  .ttl {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .kind {
    font-size: 11.5px;
    color: var(--tm-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .facts {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 3px;
    font-size: 13px;
  }
  .facts li {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .muted {
    color: var(--tm-muted);
  }
  .warn {
    color: var(--tm-warn);
  }
  .comment {
    margin: 0;
    font-size: 13px;
    font-style: italic;
    color: var(--tm-muted);
  }
  .status {
    margin: 0;
    font-size: 13px;
    font-weight: 600;
  }
  .status[data-status='ACCEPTED'] {
    color: var(--tm-success);
  }
  .status[data-status='DECLINED'] {
    color: var(--tm-danger);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .btn.on {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
    border-color: var(--tm-accent);
  }
  .propose {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: flex-end;
  }
  .compact {
    padding: 10px 12px;
  }
</style>
