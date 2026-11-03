<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Right info panel: person/group details, channels, business card from the last signature, shared media/files/links. -->
<script lang="ts">
  import X from '@lucide/svelte/icons/x';
  import Mail from '@lucide/svelte/icons/mail';
  import Phone from '@lucide/svelte/icons/phone';
  import Globe from '@lucide/svelte/icons/globe';
  import MapPin from '@lucide/svelte/icons/map-pin';
  import IdCard from '@lucide/svelte/icons/id-card';
  import UserPlus from '@lucide/svelte/icons/user-plus';
  import BellOff from '@lucide/svelte/icons/bell-off';
  import Ban from '@lucide/svelte/icons/ban';
  import Rows3 from '@lucide/svelte/icons/rows-3';
  import Link from '@lucide/svelte/icons/link';
  import { app } from '../../state';
  import { parseSignature } from '../../conversation';
  import { formatListDate, t } from '../../i18n/index.svelte';
  import type { Attachment, PersonHandle } from '../../types';
  import Avatar from '../common/Avatar.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';
  import AttachmentCard from '../common/AttachmentCard.svelte';

  type Tab = 'media' | 'files' | 'links';
  let tab = $state<Tab>('media');

  const conv = $derived(app.conversation);
  const person = $derived(conv && !conv.isGroup ? app.person(conv.participantIds[0]) : undefined);
  const members = $derived(conv?.isGroup ? conv.participantIds.map((id) => app.person(id)).filter((p) => !!p) : []);

  /** Latest incoming signature of this person -> business card. */
  const card = $derived.by(() => {
    if (!person) return null;
    const last = [...app.items].reverse().find((i) => !i.isOwn && i.senderPersonId === person.id && i.signature);
    return last?.signature ? { card: parseSignature(last.signature), at: last.sentAt } : null;
  });
  const known = $derived.by(() => {
    if (!person || !card) return false;
    const values = new Set(person.handles.map((h) => h.value.replace(/[^\d+]/g, '')));
    return card.card.phones.every((p) => values.has(p.replace(/[^\d+]/g, '')));
  });

  const media = $derived(app.items.flatMap((i) => i.attachments.filter((a) => a.previewUrl).map((a) => ({ a, item: i }))));
  const files = $derived(app.items.flatMap((i) => i.attachments.filter((a) => !a.previewUrl && !a.mimeType.startsWith('audio/')).map((a) => ({ a, item: i }))));
  const links = $derived.by(() => {
    const out: { url: string; title?: string; at: string }[] = [];
    for (const i of app.items) {
      if (i.linkPreview) out.push({ url: i.linkPreview.url, title: i.linkPreview.title, at: i.sentAt });
      for (const m of i.body.matchAll(/\bhttps?:\/\/[^\s<>"']+/g)) if (!out.some((l) => l.url === m[0])) out.push({ url: m[0], at: i.sentAt });
    }
    return out;
  });

  function handleIcon(h: PersonHandle) {
    return h.kind;
  }

  function jump(item: { id: string }) {
    app.focusItem(item.id);
    app.timelineScrollTarget = item.id;
    app.timelineScrollTick++;
  }
  function attKey(a: Attachment, i: number) {
    return `${a.id}-${i}`;
  }
</script>

<aside class="info" aria-label={t('chat.info.title')}>
  <header>
    <button class="icon-btn" aria-label={t('common.action.close')} onclick={() => (app.infoOpen = false)}><X size={18} /></button>
    <h2>{conv?.isGroup ? t('chat.info.groupTitle') : t('chat.info.title')}</h2>
  </header>
  {#if conv}
    <div class="scroll">
      <div class="hero">
        <Avatar name={conv.title} seed={conv.isGroup ? conv.title : (person?.handles[0]?.value ?? conv.title)} size={96} group={conv.isGroup && conv.kind === 'chat'} url={conv.avatarUrl} />
        <h3>{conv.title}</h3>
        {#if conv.isGroup}<p class="muted">{t('chat.info.members', { count: members.length + 1 })}</p>{/if}
      </div>

      {#if person}
        <section class="sec">
          <h4>{t('chat.info.channels')}</h4>
          <ul class="handles">
            {#each person.handles as h (h.kind + h.value)}
              {@const k = handleIcon(h)}
              <li>
                {#if k === 'email'}<Mail size={15} />{:else if k === 'phone'}<Phone size={15} />{:else}<ChannelIcon channel={k} size={15} />{/if}
                <span class="hv">{h.value}</span>
                <span class="hk">{k === 'email' || k === 'phone' ? t(`chat.info.kind.${k}`) : t(`common.channel.${k}`)}</span>
              </li>
            {/each}
          </ul>
        </section>
      {/if}

      {#if card}
        {@const c = card.card}
        <section class="sec vcard" aria-label={t('chat.info.card')}>
          <h4><IdCard size={14} /> {t('chat.info.card')}</h4>
          <div class="vc">
            {#if c.name}<strong>{c.name}</strong>{/if}
            {#if c.title}<span>{c.title}</span>{/if}
            {#if c.company}<span class="company">{c.company}</span>{/if}
            {#each c.phones as p (p)}<span class="line"><Phone size={13} /> {p}</span>{/each}
            {#each c.emails as e (e)}<span class="line"><Mail size={13} /> {e}</span>{/each}
            {#if c.address}<span class="line"><MapPin size={13} /> {c.address}</span>{/if}
            {#if c.website}<span class="line"><Globe size={13} /> {c.website}</span>{/if}
          </div>
          <p class="src">{t('chat.info.cardSource', { date: formatListDate(card.at) })}</p>
          <button class="btn" onclick={() => person && app.saveContactCard(person.id, c)}>
            <UserPlus size={15} />
            {known ? t('chat.info.updateContact') : t('chat.info.addContact')}
          </button>
        </section>
      {/if}

      {#if members.length}
        <section class="sec">
          <h4>{t('chat.info.memberList')}</h4>
          <ul class="members">
            {#each members as m (m.id)}
              <li><Avatar name={m.displayName} seed={m.id} size={30} /> <span>{m.displayName}</span></li>
            {/each}
          </ul>
        </section>
      {/if}

      <section class="sec">
        <div class="tabs" role="tablist" aria-label={t('chat.info.shared')}>
          {#each ['media', 'files', 'links'] as const as tb (tb)}
            <button role="tab" aria-selected={tab === tb} onclick={() => (tab = tb)}>{t(`chat.info.tab.${tb}`)}</button>
          {/each}
        </div>
        <div role="tabpanel">
          {#if tab === 'media'}
            {#if media.length}
              <div class="media-grid">
                {#each media as m, i (attKey(m.a, i))}
                  <button class="mthumb" onclick={() => jump(m.item)} aria-label={m.a.filename}><img src={m.a.previewUrl} alt={m.a.filename} /></button>
                {/each}
              </div>
            {:else}<p class="muted">{t('chat.info.none')}</p>{/if}
          {:else if tab === 'files'}
            {#if files.length}
              <div class="files">
                {#each files as f, i (attKey(f.a, i))}<AttachmentCard attachment={f.a} compact />{/each}
              </div>
            {:else}<p class="muted">{t('chat.info.none')}</p>{/if}
          {:else if links.length}
            <ul class="links">
              {#each links as l (l.url)}
                <li><Link size={13} /> <a href={l.url} target="_blank" rel="noopener noreferrer">{l.title ?? l.url}</a></li>
              {/each}
            </ul>
          {:else}<p class="muted">{t('chat.info.none')}</p>{/if}
        </div>
      </section>

      <section class="sec actions">
        <label class="toggle">
          <BellOff size={16} />
          <span>{t('chat.action.mute')}</span>
          <input type="checkbox" checked={conv.muted} onchange={(e) => app.updateConversation(conv.id, { muted: (e.currentTarget as HTMLInputElement).checked })} />
        </label>
        {#if !conv.isGroup}
          <label class="toggle danger">
            <Ban size={16} />
            <span>{t('chat.info.block')}</span>
            <input type="checkbox" checked={!!conv.blocked} onchange={(e) => app.updateConversation(conv.id, { blocked: (e.currentTarget as HTMLInputElement).checked })} />
          </label>
        {/if}
        <button class="btn" onclick={() => app.openInClassic(app.items.find((i) => i.id === app.focusedItemId) ?? null)}><Rows3 size={15} /> {t('chat.openInClassic')}</button>
      </section>
    </div>
  {/if}
</aside>

<style>
  .info {
    display: flex;
    flex-direction: column;
    min-height: 0;
    background: var(--tm-surface);
    border-left: 1px solid var(--tm-line);
  }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0 12px;
    min-height: 58px;
    border-bottom: 1px solid var(--tm-line);
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  .scroll {
    position: relative;
    flex: 1;
    overflow-y: auto;
  }
  .hero {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 22px 16px 14px;
    border-bottom: 8px solid var(--tm-surface-2);
  }
  h3 {
    margin: 12px 0 2px;
    font-size: 18px;
    font-weight: 650;
    text-align: center;
  }
  .muted {
    margin: 0;
    color: var(--tm-muted);
    font-size: 13px;
  }
  .sec {
    padding: 12px 16px;
    border-bottom: 8px solid var(--tm-surface-2);
  }
  h4 {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0 0 8px;
    font-size: 12px;
    font-weight: 650;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--tm-faint);
  }
  .handles,
  .members,
  .links {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .handles li,
  .members li,
  .links li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 5px 0;
    font-size: 13.5px;
    min-width: 0;
  }
  .handles :global(svg) {
    color: var(--tm-muted);
    flex: none;
  }
  .hv {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .hk {
    font-size: 11.5px;
    color: var(--tm-faint);
  }
  .vc {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 10px 12px;
    border-radius: 10px;
    background: var(--tm-surface-2);
    border-left: 3px solid var(--tm-brand);
    font-size: 13px;
  }
  .vc strong {
    font-size: 14px;
  }
  .company {
    font-weight: 600;
  }
  .line {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--tm-muted);
  }
  .src {
    margin: 6px 0 8px;
    font-size: 11.5px;
    color: var(--tm-faint);
  }
  .tabs {
    display: flex;
    gap: 4px;
    margin-bottom: 10px;
  }
  .tabs button {
    flex: 1;
    height: 30px;
    border: none;
    border-bottom: 2px solid transparent;
    background: transparent;
    color: var(--tm-muted);
    font: inherit;
    font-size: 13px;
    font-weight: 600;
  }
  .tabs button[aria-selected='true'] {
    color: var(--tm-accent-text);
    border-bottom-color: var(--tm-brand);
  }
  .media-grid {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 4px;
  }
  .mthumb {
    padding: 0;
    border: none;
    aspect-ratio: 1;
    border-radius: 6px;
    overflow: hidden;
    background: var(--tm-surface-3);
  }
  .mthumb img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .files {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .links a {
    color: var(--tm-accent-text);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .actions {
    display: flex;
    flex-direction: column;
    gap: 8px;
    border-bottom: none;
  }
  .toggle {
    display: flex;
    align-items: center;
    gap: 10px;
    font-size: 14px;
  }
  .toggle span {
    flex: 1;
  }
  .toggle input {
    accent-color: var(--tm-accent);
    width: 16px;
    height: 16px;
  }
  .toggle.danger {
    color: var(--tm-danger);
  }
</style>
