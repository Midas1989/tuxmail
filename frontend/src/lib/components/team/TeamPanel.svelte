<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Team conversation bar (assignee, status, priority, due date), collision
     hints (who views/answers, colleagues' drafts) and the internal comment
     thread with @mentions (KONZEPT 8.14). Shown in the conversation header
     (chat/hybrid) and the reading pane (classic) while a team mailbox is open. -->
<script lang="ts">
  import UserCheck from '@lucide/svelte/icons/user-check';
  import MessageSquareLock from '@lucide/svelte/icons/message-square-lock';
  import Eye from '@lucide/svelte/icons/eye';
  import PenLine from '@lucide/svelte/icons/pen-line';
  import Trash from '@lucide/svelte/icons/trash';
  import Send from '@lucide/svelte/icons/send';
  import FileText from '@lucide/svelte/icons/file-text';
  import { app, team } from '../../state';
  import { t, fmt } from '../../i18n/index.svelte';
  import type { TeamPriority, TeamStatus, TeamMember } from '../../api/team';
  import { activeMention, insertMention, mentionSegments } from './mentions';

  interface Props {
    conversationId?: number | null;
    /** Classic view: the conversation is resolved from the message. */
    messageId?: number | null;
  }
  let { conversationId = null, messageId = null }: Props = $props();

  let convId = $state<number | null>(null);
  $effect(() => {
    const cid = conversationId;
    const mid = messageId;
    if (cid != null) convId = cid;
    else if (mid != null) void app.api.conversationForMessage(mid, 'thread').then((r) => (convId = r.conversationId)).catch(() => (convId = null));
    else convId = null;
  });
  $effect(() => {
    const id = convId;
    void team.activeId;
    void team.open(id);
    return () => void team.open(null);
  });

  // Typing in a composer marks the user as "replying" (collision hint).
  $effect(() => {
    const onInput = (e: Event) => {
      const el = e.target as HTMLElement | null;
      if (!el || el.closest('.team-panel')) return;
      if (el instanceof HTMLTextAreaElement || el.isContentEditable) team.noteTyping();
    };
    document.addEventListener('input', onInput, true);
    return () => document.removeEventListener('input', onInput, true);
  });

  const d = $derived(team.detail);
  const a = $derived(d?.team);
  const me = $derived(team.myId);
  const workers = $derived(team.members.filter((m) => m.role !== 'read'));
  const canAssignOthers = $derived(team.can('assign'));
  const mineOrFree = $derived(!a?.assigneeId || a.assigneeId === me);
  const canEdit = $derived(team.can('reply') && (canAssignOthers || mineOrFree));
  const statuses: TeamStatus[] = ['open', 'in_progress', 'waiting', 'done'];
  const priorities: TeamPriority[] = ['low', 'normal', 'high', 'urgent'];
  const replying = $derived(team.others.filter((p) => p.state === 'replying'));
  const viewing = $derived(team.others.filter((p) => p.state === 'viewing'));
  const overdue = $derived(!!a?.dueAt && a.status !== 'done' && new Date(a.dueAt).getTime() < Date.now());

  let commentsOpen = $state(true);
  let body = $state('');
  let sending = $state(false);
  let caret = $state(0);
  let input = $state<HTMLTextAreaElement>();
  let pick = $state(0);
  let draftOpen = $state<number | null>(null);

  const mention = $derived(activeMention(body, caret));
  const suggestions = $derived(
    mention == null ? [] : team.members.filter((m) => m.userId !== me && (m.username.startsWith(mention.query) || m.displayName.toLowerCase().includes(mention.query))).slice(0, 6),
  );

  function onAssignee(e: Event) {
    const v = Number((e.currentTarget as HTMLSelectElement).value);
    void team.assign({ assigneeId: v });
  }

  function choose(m: TeamMember) {
    if (!mention) return;
    const r = insertMention(body, mention, m.username);
    body = r.text;
    requestAnimationFrame(() => {
      input?.focus();
      input?.setSelectionRange(r.caret, r.caret);
      caret = r.caret;
    });
  }

  function onKey(e: KeyboardEvent) {
    if (suggestions.length) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        pick = (pick + (e.key === 'ArrowDown' ? 1 : suggestions.length - 1)) % suggestions.length;
        return;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        choose(suggestions[pick] ?? suggestions[0]);
        return;
      }
    }
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      void submit();
    }
  }

  async function submit() {
    const text = body.trim();
    if (!text || sending) return;
    sending = true;
    try {
      await team.addComment(text);
      body = '';
    } catch {
      app.toast(t('team.error.permission'), undefined, 'error');
    } finally {
      sending = false;
    }
  }

  function dueValue(iso: string | undefined): string {
    return iso ? iso.slice(0, 10) : '';
  }
</script>

{#if team.active && d && a}
  <div class="team-panel" role="region" aria-label={t('team.panel', { name: team.active.name })}>
    <div class="bar">
      <span class="badge" title={team.active.address}>{team.active.name}</span>
      <label class="field">
        <span class="lbl">{t('team.assignee')}</span>
        <select class="input sel" value={String(a.assigneeId ?? 0)} onchange={onAssignee} disabled={!team.can('reply')} aria-label={t('team.assignee')}>
          <option value="0">{t('team.unassigned')}</option>
          {#each workers as m (m.userId)}
            <option value={String(m.userId)} disabled={!canAssignOthers && m.userId !== me && m.userId !== a.assigneeId}>{m.displayName}{m.userId === me ? ` (${t('team.me')})` : ''}</option>
          {/each}
        </select>
      </label>
      {#if team.can('reply') && a.assigneeId !== me && (canAssignOthers || !a.assigneeId) && me != null}
        <button class="btn btn-ghost take" onclick={() => team.takeOver(me!)}><UserCheck size={15} /> {t('team.assignToMe')}</button>
      {/if}
      <label class="field">
        <span class="lbl">{t('team.status.label')}</span>
        <select class="input sel status s-{a.status}" value={a.status} disabled={!canEdit} onchange={(e) => team.assign({ status: (e.currentTarget as HTMLSelectElement).value as TeamStatus })} aria-label={t('team.status.label')}>
          {#each statuses as s (s)}<option value={s}>{t(`team.status.${s}`)}</option>{/each}
        </select>
      </label>
      <label class="field">
        <span class="lbl">{t('team.priority.label')}</span>
        <select class="input sel prio p-{a.priority}" value={a.priority} disabled={!canEdit} onchange={(e) => team.assign({ priority: (e.currentTarget as HTMLSelectElement).value as TeamPriority })} aria-label={t('team.priority.label')}>
          {#each priorities as p (p)}<option value={p}>{t(`team.priority.${p}`)}</option>{/each}
        </select>
      </label>
      <label class="field">
        <span class="lbl">{t('team.due')}</span>
        <input type="date" class="input date" class:overdue value={dueValue(a.dueAt)} disabled={!canEdit} onchange={(e) => team.assign({ dueAt: (e.currentTarget as HTMLInputElement).value })} aria-label={t('team.due')} />
      </label>
      <span class="grow"></span>
      <button class="btn btn-ghost" aria-expanded={commentsOpen} onclick={() => (commentsOpen = !commentsOpen)} title={t('team.comments.toggle')}>
        <MessageSquareLock size={15} /> {t('team.comments.count', { count: team.comments.length })}
      </button>
    </div>

    {#if replying.length || viewing.length || d.drafts.length}
      <div class="presence" role="status" aria-live="polite">
        {#each replying as p (p.userId)}
          <span class="pchip replying"><PenLine size={13} /> {t('team.presence.replying', { name: p.userName })}</span>
        {/each}
        {#each viewing as p (p.userId)}
          <span class="pchip"><Eye size={13} /> {t('team.presence.viewing', { name: p.userName })}</span>
        {/each}
        {#each d.drafts as dr (dr.userId)}
          <button class="pchip draft" onclick={() => (draftOpen = draftOpen === dr.userId ? null : dr.userId)} aria-expanded={draftOpen === dr.userId} disabled={!dr.body}>
            <FileText size={13} /> {t('team.presence.draft', { name: dr.userName, when: fmt().relative(dr.updatedAt) })}
          </button>
        {/each}
      </div>
      {#each d.drafts as dr (dr.userId)}
        {#if draftOpen === dr.userId && dr.body}
          <blockquote class="shared-draft">{dr.body.text}</blockquote>
        {/if}
      {/each}
    {/if}

    {#if commentsOpen}
      <div class="comments">
        <p class="internal"><MessageSquareLock size={13} /> {t('team.comments.internal')}</p>
        {#each team.comments as c (c.id)}
          <article class="note" class:mine={c.authorId === me} class:mentioned={me != null && c.mentions.includes(me)}>
            <header>
              <strong>{c.authorName}</strong>
              <time datetime={c.createdAt}>{fmt().relative(c.createdAt)}</time>
              {#if c.authorId === me || team.can('manage')}
                <button class="icon-btn del" aria-label={t('team.comments.delete')} onclick={() => team.deleteComment(c.id)}><Trash size={13} /></button>
              {/if}
            </header>
            <p>{#each mentionSegments(c.body) as seg, i (i)}{#if seg.mention}<span class="at">@{seg.text}</span>{:else}{seg.text}{/if}{/each}</p>
          </article>
        {/each}
        {#if team.can('reply')}
          <div class="new">
            <textarea
              bind:this={input}
              bind:value={body}
              class="input"
              rows="2"
              placeholder={t('team.comments.placeholder')}
              aria-label={t('team.comments.placeholder')}
              onkeydown={onKey}
              oninput={(e) => ((caret = (e.currentTarget as HTMLTextAreaElement).selectionStart), (pick = 0))}
              onclick={(e) => (caret = (e.currentTarget as HTMLTextAreaElement).selectionStart)}
            ></textarea>
            <button class="btn btn-primary" disabled={!body.trim() || sending} onclick={submit} aria-label={t('team.comments.add')}><Send size={15} /></button>
            {#if suggestions.length}
              <ul class="suggest" role="listbox" aria-label={t('team.comments.mention')}>
                {#each suggestions as m, i (m.userId)}
                  <li role="option" aria-selected={i === pick}>
                    <button onmousedown={(e) => (e.preventDefault(), choose(m))}><strong>{m.displayName}</strong> <span>@{m.username}</span></button>
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {/if}
      </div>
    {/if}
    {#if overdue}<span class="sr-only">{t('team.overdue')}</span>{/if}
  </div>
{/if}

<style>
  .team-panel {
    border-bottom: 1px solid var(--tm-line);
    background: var(--tm-surface);
    font-size: 13px;
  }
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 6px 10px;
    padding: 8px 14px;
  }
  .badge {
    align-self: center;
    padding: 2px 8px;
    border-radius: 6px;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
    font-weight: 650;
    font-size: 12px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .lbl {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--tm-faint);
  }
  .sel,
  .date {
    height: 30px;
    padding: 0 8px;
    font-size: 13px;
    min-width: 0;
  }
  .status.s-done {
    color: var(--tm-success);
  }
  .status.s-waiting {
    color: var(--tm-warn);
  }
  .prio.p-urgent,
  .prio.p-high,
  .date.overdue {
    color: var(--tm-danger);
    font-weight: 600;
  }
  .take {
    height: 30px;
  }
  .grow {
    flex: 1;
  }
  .presence {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 0 14px 8px;
  }
  .pchip {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 3px 9px;
    border: 1px solid var(--tm-line);
    border-radius: 12px;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
    font: inherit;
    font-size: 12px;
  }
  .pchip.replying {
    border-color: color-mix(in srgb, var(--tm-warn) 60%, transparent);
    background: color-mix(in srgb, var(--tm-warn) 16%, var(--tm-surface));
    color: var(--tm-fg);
    font-weight: 600;
  }
  .pchip.draft {
    cursor: pointer;
  }
  .shared-draft {
    margin: 0 14px 8px;
    padding: 8px 10px;
    border-left: 3px solid var(--tm-line-strong);
    color: var(--tm-muted);
    white-space: pre-wrap;
  }
  .comments {
    padding: 4px 14px 10px;
    max-height: 38vh;
    overflow-y: auto;
  }
  .internal {
    display: flex;
    align-items: center;
    gap: 5px;
    margin: 0 0 6px;
    font-size: 11.5px;
    color: var(--tm-faint);
  }
  /* Internal notes look like sticky notes, never like mail bubbles. */
  .note {
    margin: 0 0 6px;
    padding: 6px 10px;
    border: 1px dashed color-mix(in srgb, var(--tm-warn) 70%, transparent);
    border-radius: 8px;
    background: color-mix(in srgb, var(--tm-warn) 11%, var(--tm-surface));
  }
  .note.mentioned {
    border-style: solid;
  }
  .note header {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12px;
  }
  .note time {
    color: var(--tm-faint);
  }
  .note .del {
    margin-left: auto;
    width: 22px;
    height: 22px;
  }
  .note p {
    margin: 2px 0 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .at {
    padding: 0 3px;
    border-radius: 4px;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
    font-weight: 600;
  }
  .new {
    position: relative;
    display: flex;
    gap: 6px;
    align-items: flex-end;
  }
  .new textarea {
    flex: 1;
    resize: vertical;
    min-height: 38px;
    font-size: 13px;
  }
  .suggest {
    position: absolute;
    left: 0;
    bottom: 100%;
    z-index: 20;
    margin: 0 0 4px;
    padding: 4px;
    list-style: none;
    min-width: 220px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    box-shadow: var(--tm-shadow-lg);
  }
  .suggest button {
    display: flex;
    gap: 6px;
    width: 100%;
    padding: 5px 8px;
    border: 0;
    border-radius: 6px;
    background: none;
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .suggest [aria-selected='true'] button,
  .suggest button:hover {
    background: var(--tm-accent-soft);
  }
  .suggest span {
    color: var(--tm-muted);
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
</style>
