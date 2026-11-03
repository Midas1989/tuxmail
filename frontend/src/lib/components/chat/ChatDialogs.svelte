<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Dialogs of the chat features: folder editor, snooze, starred, scheduled, focus settings. -->
<script lang="ts">
  import Trash from '@lucide/svelte/icons/trash';
  import Star from '@lucide/svelte/icons/star';
  import Clock from '@lucide/svelte/icons/clock';
  import Send from '@lucide/svelte/icons/send';
  import Pencil from '@lucide/svelte/icons/pencil';
  import { app } from '../../state';
  import { fmt, formatListDate, t } from '../../i18n/index.svelte';
  import { EMPTY_RULES, timePresets, toLocalInput } from '../../chatfeatures';
  import type { Channel, ChatFolder, FocusSettings, ScheduledMessage, StarredItem } from '../../types';
  import Modal from '../common/Modal.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';

  const d = $derived(app.chatDialog);
  const close = () => (app.chatDialog = null);
  const CHANNELS: Channel[] = ['email', 'whatsapp', 'signal', 'telegram'];

  // ----- folder editor --------------------------------------------------------
  let folder = $state<ChatFolder>({ id: '', name: '', emoji: '', order: 99, rules: { ...EMPTY_RULES } });
  let allConvs = $state<{ id: number; title: string; channel?: Channel }[]>([]);
  $effect(() => {
    if (d?.kind !== 'folder') return;
    folder = d.folder ? structuredClone($state.snapshot(d.folder) as ChatFolder) : { id: '', name: '', emoji: '', order: app.chatFolders.length, rules: { ...EMPTY_RULES } };
    void app.api.listConversations({ scope: { kind: 'all' }, grouping: 'person', offset: 0, limit: 500 }).then((p) => (allConvs = p.items.map((c) => ({ id: c.id, title: c.title, channel: c.lastChannel }))));
  });
  function toggle<T>(list: T[], v: T): T[] {
    return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
  }
  async function saveFolder() {
    if (!folder.name.trim()) return;
    await app.saveChatFolder($state.snapshot(folder) as ChatFolder);
    close();
  }

  // ----- snooze -----------------------------------------------------------------
  let custom = $state(toLocalInput(new Date(Date.now() + 3 * 3600_000)));
  const presets = $derived(timePresets(new Date(app.now)));

  // ----- starred / scheduled -------------------------------------------------------
  let starred = $state<StarredItem[]>([]);
  let scheduled = $state<ScheduledMessage[]>([]);
  let editing = $state<{ id: string; text: string; when: string } | null>(null);
  $effect(() => {
    if (d?.kind === 'starred') void app.api.listStarredItems().then((s) => (starred = s));
    if (d?.kind === 'scheduled') void loadScheduled();
  });
  async function loadScheduled() {
    scheduled = await app.api.listScheduled();
  }
  async function open(conversationId: number, itemId: string) {
    close();
    await app.revealItem(conversationId, itemId);
  }

  // ----- focus -----------------------------------------------------------------------
  let focus = $state<FocusSettings>({ enabled: false, vipPersonIds: [], vipConversationIds: [], includeFrequent: true });
  $effect(() => {
    if (d?.kind === 'focus') focus = structuredClone($state.snapshot(app.focus) as FocusSettings);
  });
  const people = $derived(app.persons.filter((p) => !p.isMe && p.handles.some((h) => h.kind !== 'email' || !/noreply|newsletter|rechnung|service|bestellung|win@|it@/.test(h.value))));
  const days = [1, 2, 3, 4, 5, 6, 0];
  const dayName = (n: number) => new Intl.DateTimeFormat(fmt().locale, { weekday: 'short' }).format(new Date(2026, 10, 1 + n)); // 1 Nov 2026 = Sunday
</script>

{#if d?.kind === 'folder'}
  <Modal title={d.folder ? t('chat.folders.edit') : t('chat.folders.new')} onclose={close} width="560px">
    <div class="form">
      <div class="row2">
        <label class="emoji"><span class="label">{t('chat.folders.emoji')}</span><input class="input" maxlength="4" bind:value={folder.emoji} /></label>
        <label class="grow"><span class="label">{t('chat.folders.name')}</span><input class="input" bind:value={folder.name} data-autofocus /></label>
      </div>
      <fieldset>
        <legend>{t('chat.folders.channels')}</legend>
        <div class="chips">
          {#each CHANNELS as ch (ch)}
            <label class="chk"><input type="checkbox" checked={folder.rules.channels.includes(ch)} onchange={() => (folder.rules.channels = toggle(folder.rules.channels, ch))} /> <ChannelIcon channel={ch} size={14} label /></label>
          {/each}
        </div>
      </fieldset>
      <fieldset>
        <legend>{t('chat.folders.accounts')}</legend>
        <div class="chips">
          {#each app.accounts as a (a.id)}
            <label class="chk"><input type="checkbox" checked={folder.rules.accountIds.includes(a.id)} onchange={() => (folder.rules.accountIds = toggle(folder.rules.accountIds, a.id))} /> {a.displayName}</label>
          {/each}
        </div>
      </fieldset>
      <fieldset>
        <legend>{t('chat.folders.chats')}</legend>
        <div class="list">
          {#each allConvs as c (c.id)}
            <label class="chk"><input type="checkbox" checked={folder.rules.includeConversationIds.includes(c.id)} onchange={() => (folder.rules.includeConversationIds = toggle(folder.rules.includeConversationIds, c.id))} /> {c.title}</label>
          {/each}
        </div>
      </fieldset>
      <fieldset>
        <legend>{t('chat.folders.options')}</legend>
        <label class="chk">
          {t('chat.folders.groups')}
          <select class="input small" bind:value={folder.rules.groups}>
            <option value="any">{t('chat.folders.groupsAny')}</option>
            <option value="only">{t('chat.folders.groupsOnly')}</option>
            <option value="exclude">{t('chat.folders.groupsExclude')}</option>
          </select>
        </label>
        <label class="chk"><input type="checkbox" bind:checked={folder.rules.unreadOnly} /> {t('chat.folders.unreadOnly')}</label>
        <label class="chk"><input type="checkbox" bind:checked={folder.rules.excludeMuted} /> {t('chat.folders.excludeMuted')}</label>
        <label class="chk"><input type="checkbox" bind:checked={folder.rules.excludeArchived} /> {t('chat.folders.excludeArchived')}</label>
      </fieldset>
    </div>
    {#snippet footer()}
      {#if d.folder}
        <button
          class="btn danger-ghost"
          onclick={async () => {
            await app.deleteChatFolder(folder.id);
            close();
          }}><Trash size={14} /> {t('chat.folders.delete')}</button
        >
      {/if}
      <span class="spacer"></span>
      <button class="btn" onclick={close}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" disabled={!folder.name.trim()} onclick={saveFolder}>{t('chat.folders.save')}</button>
    {/snippet}
  </Modal>
{:else if d?.kind === 'snooze'}
  {@const conv = d.conversation}
  <Modal title={t('chat.snooze.title', { name: conv.title })} onclose={close} width="380px">
    <div class="presets">
      {#each presets as p (p.id)}
        <button
          class="preset"
          onclick={() => {
            close();
            void app.snooze(conv.id, p.at);
          }}
        >
          <span>{t(`chat.time.${p.id}`)}</span>
          <small>{fmt().dateTime(p.at)}</small>
        </button>
      {/each}
    </div>
    <label class="custom"><span class="label">{t('chat.time.custom')}</span><input class="input" type="datetime-local" bind:value={custom} /></label>
    {#snippet footer()}
      <button class="btn" onclick={close}>{t('common.action.cancel')}</button>
      <button
        class="btn btn-primary"
        onclick={() => {
          close();
          void app.snooze(conv.id, new Date(custom));
        }}><Clock size={14} /> {t('chat.snooze.action')}</button
      >
    {/snippet}
  </Modal>
{:else if d?.kind === 'starred'}
  <Modal title={t('chat.starred.title')} onclose={close} width="560px">
    {#if starred.length}
      <ul class="results">
        {#each starred as s (s.item.id)}
          <li>
            <button onclick={() => open(s.item.conversationId, s.item.id)}>
              <Star size={15} class="star-ic" />
              <span class="r-main">
                <span class="r-title">{s.conversationTitle} · {s.item.isOwn ? t('chat.you') : s.item.senderName}</span>
                <span class="r-text">{s.item.body || s.item.card?.title || s.item.attachments[0]?.filename}</span>
              </span>
              <time>{formatListDate(s.item.sentAt)}</time>
            </button>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="muted">{t('chat.starred.empty')}</p>
    {/if}
  </Modal>
{:else if d?.kind === 'scheduled'}
  <Modal title={t('chat.schedule.overview')} onclose={close} width="600px">
    {#if scheduled.length}
      <ul class="results">
        {#each scheduled as s (s.id)}
          <li class="sched">
            {#if editing?.id === s.id}
              <div class="edit">
                <textarea class="input" rows="2" bind:value={editing.text}></textarea>
                <input class="input" type="datetime-local" bind:value={editing.when} />
                <div class="edit-actions">
                  <button class="btn" onclick={() => (editing = null)}>{t('common.action.cancel')}</button>
                  <button
                    class="btn btn-primary"
                    onclick={async () => {
                      await app.api.updateScheduled(s.id, { text: editing!.text, sendAt: new Date(editing!.when).toISOString() });
                      editing = null;
                      await loadScheduled();
                      await app.reloadItems();
                    }}>{t('chat.schedule.save')}</button
                  >
                </div>
              </div>
            {:else}
              <button class="r-open" onclick={() => open(s.conversationId, s.itemId)}>
                <ChannelIcon channel={s.channel} size={15} />
                <span class="r-main">
                  <span class="r-title">{s.conversationTitle}</span>
                  <span class="r-text">{s.text}</span>
                </span>
                <span class="when"><Clock size={12} /> {fmt().dateTime(s.sendAt)}</span>
              </button>
              <button class="icon-btn" aria-label={t('chat.schedule.edit')} onclick={() => (editing = { id: s.id, text: s.text, when: toLocalInput(new Date(s.sendAt)) })}><Pencil size={15} /></button>
              <button
                class="icon-btn"
                aria-label={t('chat.schedule.sendNow')}
                onclick={async () => {
                  await app.api.sendScheduledNow(s.id);
                  await loadScheduled();
                  await app.reloadItems();
                }}><Send size={15} /></button
              >
              <button
                class="icon-btn"
                aria-label={t('chat.schedule.cancel')}
                onclick={async () => {
                  await app.api.cancelScheduled(s.id);
                  await loadScheduled();
                  await app.reloadItems();
                }}><Trash size={15} /></button
              >
            {/if}
          </li>
        {/each}
      </ul>
    {:else}
      <p class="muted">{t('chat.schedule.empty')}</p>
    {/if}
  </Modal>
{:else if d?.kind === 'focus'}
  <Modal title={t('chat.focus.title')} onclose={close} width="560px">
    <div class="form">
      <p class="muted">{t('chat.focus.intro')}</p>
      <label class="chk"><input type="checkbox" bind:checked={focus.enabled} /> {t('chat.focus.enabled')}</label>
      <label class="chk"><input type="checkbox" bind:checked={focus.includeFrequent} /> {t('chat.focus.frequent')}</label>
      <fieldset>
        <legend>{t('chat.focus.vip')}</legend>
        <div class="list">
          {#each people as p (p.id)}
            <label class="chk"><input type="checkbox" checked={focus.vipPersonIds.includes(p.id)} onchange={() => (focus.vipPersonIds = toggle(focus.vipPersonIds, p.id))} /> {p.displayName}{#if app.frequentIds.includes(p.id)}<small class="freq">{t('chat.focus.frequentBadge')}</small>{/if}</label>
          {/each}
        </div>
      </fieldset>
      <fieldset>
        <legend>{t('chat.focus.schedule')}</legend>
        {#if focus.schedule}
          <label class="chk"><input type="checkbox" bind:checked={focus.schedule.enabled} /> {t('chat.focus.scheduleEnabled')}</label>
          <div class="row2">
            <label><span class="label">{t('notifications.from')}</span><input class="input" type="time" bind:value={focus.schedule.start} disabled={!focus.schedule.enabled} /></label>
            <label><span class="label">{t('notifications.to')}</span><input class="input" type="time" bind:value={focus.schedule.end} disabled={!focus.schedule.enabled} /></label>
          </div>
          <div class="chips">
            {#each days as dn (dn)}
              <label class="chk"><input type="checkbox" checked={focus.schedule.days.includes(dn)} disabled={!focus.schedule.enabled} onchange={() => (focus.schedule!.days = toggle(focus.schedule!.days, dn))} /> {dayName(dn)}</label>
            {/each}
          </div>
        {/if}
      </fieldset>
    </div>
    {#snippet footer()}
      <button class="btn" onclick={close}>{t('common.action.cancel')}</button>
      <button
        class="btn btn-primary"
        onclick={async () => {
          await app.saveFocus($state.snapshot(focus) as FocusSettings);
          close();
        }}>{t('chat.folders.save')}</button
      >
    {/snippet}
  </Modal>
{/if}

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .row2 {
    display: flex;
    gap: 10px;
  }
  .row2 > * {
    flex: 1;
  }
  .emoji {
    flex: 0 0 80px;
  }
  .grow {
    flex: 1;
  }
  fieldset {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 8px 12px 10px;
    margin: 0;
  }
  legend {
    font-size: 12.5px;
    font-weight: 600;
    padding: 0 4px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
  }
  .list {
    max-height: 180px;
    overflow-y: auto;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 2px 12px;
  }
  .chk {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 3px 0;
    font-size: 13.5px;
  }
  .chk input {
    accent-color: var(--tm-accent);
  }
  .small {
    width: auto;
    height: 28px;
    margin-left: auto;
  }
  .freq {
    margin-left: 4px;
    font-size: 10.5px;
    color: var(--tm-accent-text);
    background: var(--tm-accent-soft);
    padding: 0 6px;
    border-radius: 99px;
  }
  .spacer {
    flex: 1;
  }
  .danger-ghost {
    color: var(--tm-danger);
  }
  .presets {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-bottom: 12px;
  }
  .preset {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 10px 12px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    font-size: 14px;
  }
  .preset:hover {
    background: var(--tm-surface-2);
  }
  .preset small {
    color: var(--tm-muted);
  }
  .custom {
    display: block;
  }
  .results {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .results li {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .results li > button:not(.icon-btn),
  .r-open {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border: none;
    border-radius: 10px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
  }
  .results li > button:not(.icon-btn):hover {
    background: var(--tm-surface-2);
  }
  .results :global(.star-ic) {
    color: #f59e0b;
    fill: #f59e0b;
    flex: none;
  }
  .r-main {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .r-title {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .r-text {
    font-size: 13.5px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  time,
  .when {
    font-size: 12px;
    color: var(--tm-muted);
    white-space: nowrap;
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .edit {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 6px 0;
  }
  .edit textarea {
    height: auto;
    padding: 6px 10px;
  }
  .edit-actions {
    display: flex;
    justify-content: flex-end;
    gap: 6px;
  }
  .muted {
    margin: 0;
    color: var(--tm-muted);
    font-size: 13px;
  }
</style>
