<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import { needsOnline } from '../state/connection.svelte';
  import { t as tOffline } from '../i18n/index.svelte';
  const notOffline = () => tOffline('connection.notAvailableOffline');
  import Mail from '@lucide/svelte/icons/mail';
  import Phone from '@lucide/svelte/icons/phone';
  import UserPlus from '@lucide/svelte/icons/user-plus';
  import Users from '@lucide/svelte/icons/users';
  import BookUser from '@lucide/svelte/icons/book-user';
  import Copy from '@lucide/svelte/icons/copy';
  import MessageCircle from '@lucide/svelte/icons/message-circle';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Pencil from '@lucide/svelte/icons/pencil';
  import Trash from '@lucide/svelte/icons/trash-2';
  import MapPin from '@lucide/svelte/icons/map-pin';
  import Globe from '@lucide/svelte/icons/globe';
  import Cake from '@lucide/svelte/icons/cake';
  import Tag from '@lucide/svelte/icons/tag';
  import Plus from '@lucide/svelte/icons/plus';
  import { app } from '../state';
  import { fmt, t } from '../i18n/index.svelte';
  import { tk } from '../calendar/i18n';
  import type { ContactGroup, HandleKind, PimAddressBook, PimContact } from '../types';
  import Avatar from '../components/common/Avatar.svelte';
  import ChannelIcon from '../components/common/ChannelIcon.svelte';
  import Modal from '../components/common/Modal.svelte';
  import ContactForm, { blankContact, cleanContact } from '../components/contacts/ContactForm.svelte';
  import MergeDialog from '../components/contacts/MergeDialog.svelte';
  import { safeHref } from '../safeurl';

  let books = $state<PimAddressBook[]>([]);
  let groups = $state<ContactGroup[]>([]);
  let contacts = $state<PimContact[]>([]);
  let filter = $state('');
  let bookId = $state(0);
  let group = $state('');
  let selectedId = $state<number | null>(null);
  let editing = $state<PimContact | null>(null);
  let saving = $state(false);
  let error = $state('');
  let showMerge = $state(false);
  let confirmDelete = $state(false);
  let newBook = $state<string | null>(null);

  const selected = $derived(contacts.find((c) => c.id === selectedId) ?? null);
  const letters = $derived.by(() => {
    const m = new Map<string, PimContact[]>();
    for (const c of contacts) {
      const k = (c.n.family || c.displayName || '#').charAt(0).toLocaleUpperCase();
      const key = /\p{L}/u.test(k) ? k : '#';
      m.set(key, [...(m.get(key) ?? []), c]);
    }
    return [...m];
  });

  async function loadMeta() {
    [books, groups] = await Promise.all([app.api.listAddressBooks(), app.api.listContactGroups()]);
  }
  async function loadContacts() {
    contacts = await app.api.listContacts({ addressBookId: bookId || undefined, search: filter || undefined, group: group || undefined });
  }

  $effect(() => {
    void loadMeta();
    return app.api.subscribe((e) => {
      if (e.type === 'contacts.changed') {
        void loadMeta();
        void loadContacts();
      }
    });
  });
  $effect(() => {
    void bookId;
    void group;
    void filter;
    void loadContacts();
  });

  function startNew() {
    editing = blankContact(books.find((b) => b.isDefault)?.id ?? books[0]?.id ?? 0);
    error = '';
  }
  function startEdit(c: PimContact) {
    editing = structuredClone($state.snapshot(c)) as PimContact;
    if (!editing.emails.length) editing.emails = [{ value: '', type: 'home' }];
    if (!editing.phones.length) editing.phones = [{ value: '', type: 'cell' }];
    error = '';
  }

  async function save() {
    if (!editing) return;
    saving = true;
    error = '';
    try {
      const saved = await app.api.saveContact(cleanContact($state.snapshot(editing) as PimContact));
      editing = null;
      selectedId = saved.id;
      await Promise.all([loadContacts(), loadMeta()]);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!selected) return;
    const name = selected.displayName;
    await app.api.deleteContact(selected.id);
    confirmDelete = false;
    selectedId = null;
    app.toast(t('contacts.deleted', { name }));
    await Promise.all([loadContacts(), loadMeta()]);
  }

  async function addBook() {
    if (!newBook?.trim()) return;
    await app.api.saveAddressBook({ name: newBook.trim() });
    newBook = null;
    await loadMeta();
  }

  async function openConversation(personId: number) {
    app.navigate('mail');
    await app.setViewMode('chat');
    if (app.sel.kind !== 'unified') await app.select({ kind: 'unified', role: 'inbox' });
    await app.setGrouping('person');
    await app.loadConversations();
    await app.selectConversation(1000 + personId);
  }

  function writeMail(email: string, name: string) {
    app.openCompose({ mode: 'new', to: [{ email, name }] });
  }

  function channelOf(kind: HandleKind | string): 'whatsapp' | 'signal' | 'telegram' | null {
    return kind === 'whatsapp' || kind === 'signal' || kind === 'telegram' ? kind : null;
  }

  function imppChannel(v: string): 'whatsapp' | 'signal' | 'telegram' | null {
    const s = v.split(':')[0].toLowerCase();
    return s === 'whatsapp' ? 'whatsapp' : s === 'signal' || s === 'sgnl' ? 'signal' : s === 'telegram' || s === 'tg' ? 'telegram' : null;
  }

  function birthday(c: PimContact): string {
    const b = c.birthday;
    if (!b) return '';
    if (b.startsWith('--')) return fmt().pattern(`2000-${b.slice(2)}T12:00:00Z`, 'D. MMMM');
    const age = Number(new Date().getFullYear()) - Number(b.slice(0, 4));
    return `${fmt().dateLong(b + 'T12:00:00Z')} · ${t('calendar.age', { n: age })}`;
  }

  function addressText(a: PimContact['addresses'][number]): string {
    return [a.street, [a.postalCode, a.locality].filter(Boolean).join(' '), a.region, a.country].filter(Boolean).join(', ');
  }

  const bookOf = (c: PimContact) => books.find((b) => b.id === c.addressBookId);
</script>

<section class="cs" aria-labelledby="ct-title">
  <aside class="side" class:hidden-narrow={selectedId !== null}>
    <h1 id="ct-title">{t('nav.contacts')}</h1>
    <button use:needsOnline={notOffline} class="btn btn-primary wide" onclick={startNew}><UserPlus size={15} /> {t('contacts.add')}</button>
    <nav aria-label={t('contacts.books')}>
      <button class="nav-item" aria-current={bookId === 0 && !group} onclick={() => ((bookId = 0), (group = ''))}>
        <Users size={15} /> <span>{t('contacts.all')}</span>
        <small>{books.reduce((n, b) => n + b.count, 0)}</small>
      </button>
      <div class="sect">{t('contacts.books')}</div>
      {#each books as b (b.id)}
        <button class="nav-item" aria-current={bookId === b.id} onclick={() => ((bookId = b.id), (group = ''))} title={b.error || b.url || ''}>
          <BookUser size={15} /> <span>{b.name}{#if b.accountName}<em>{b.accountName}</em>{/if}</span>
          <small>{b.count}</small>
        </button>
      {/each}
      {#if newBook !== null}
        <form
          class="newbook"
          onsubmit={(e) => {
            e.preventDefault();
            void addBook();
          }}
        >
          <input class="input" bind:value={newBook} placeholder={t('contacts.bookName')} aria-label={t('contacts.bookName')} />
        </form>
      {:else}
        <button class="nav-item muted" onclick={() => (newBook = '')}><Plus size={15} /> <span>{t('contacts.newBook')}</span></button>
      {/if}
      {#if groups.length}
        <div class="sect">{t('contacts.groups')}</div>
        {#each groups as g (g.name)}
          <button class="nav-item" aria-current={group === g.name} onclick={() => ((group = g.name), (bookId = 0))}>
            <Tag size={14} /> <span>{g.name}</span>
            <small>{g.count}</small>
          </button>
        {/each}
      {/if}
    </nav>
    <button class="btn btn-ghost wide" onclick={() => (showMerge = true)}><Copy size={15} /> {t('contacts.duplicates')}</button>
  </aside>

  <div class="list" class:hidden-narrow={selectedId !== null}>
    <div class="lhead">
      <input class="input" type="search" placeholder={t('contacts.filter')} bind:value={filter} aria-label={t('contacts.filter')} />
    </div>
    {#if contacts.length === 0}
      <p class="empty">{t('contacts.empty')}</p>
    {/if}
    <ul>
      {#each letters as [letter, list] (letter)}
        <li class="letter" aria-hidden="true">{letter}</li>
        {#each list as c (c.id)}
          <li>
            <button class="row" aria-current={selectedId === c.id} onclick={() => (selectedId = c.id)}>
              <Avatar name={c.displayName} seed={c.emails[0]?.value ?? c.id} size={36} url={c.photoUrl} />
              <span class="rinfo">
                <span class="rn">{c.displayName}</span>
                <span class="rs">{c.org || c.emails[0]?.value || c.phones[0]?.value || ''}</span>
              </span>
              <span class="chs">
                {#each c.channels.filter((h) => channelOf(h.kind)) as h (h.kind + h.value)}<ChannelIcon channel={channelOf(h.kind)!} size={13} />{/each}
              </span>
            </button>
          </li>
        {/each}
      {/each}
    </ul>
  </div>

  <div class="detail" class:hidden-narrow={selectedId === null}>
    {#if selected}
      <div class="dhead">
        <button class="icon-btn back" aria-label={t('common.action.back')} onclick={() => (selectedId = null)}><ArrowLeft size={18} /></button>
        <Avatar name={selected.displayName} seed={selected.emails[0]?.value ?? selected.id} size={72} url={selected.photoUrl} />
        <div class="dn">
          <h2>{selected.displayName}</h2>
          {#if selected.title || selected.org}<p>{[selected.title, selected.org, selected.department].filter(Boolean).join(' · ')}</p>{/if}
          <p class="book">{bookOf(selected)?.name ?? ''}{#if bookOf(selected)?.accountName}&nbsp;· {t('contacts.syncedWith', { account: bookOf(selected)?.accountName ?? '' })}{/if}</p>
        </div>
        <div class="dact">
          <button class="btn" onclick={() => startEdit(selected!)}><Pencil size={14} /> {t('common.action.edit')}</button>
          <button class="icon-btn" aria-label={t('common.action.delete')} title={t('common.action.delete')} onclick={() => (confirmDelete = true)}><Trash size={16} /></button>
        </div>
      </div>
      <div class="quick">
        {#if selected.emails[0]}<button class="btn" onclick={() => writeMail(selected!.emails[0].value, selected!.displayName)}><Mail size={15} /> {t('contacts.writeMail')}</button>{/if}
        {#if selected.phones[0]}<a class="btn" href="tel:{selected.phones[0].value.replace(/[^\d+]/g, '')}"><Phone size={15} /> {t('contacts.call')}</a>{/if}
        {#if selected.personId}<button class="btn" onclick={() => openConversation(selected!.personId!)}><MessageCircle size={15} /> {t('contacts.openChat')}</button>{/if}
      </div>
      <div class="cards">
        <section class="card">
          <h3>{t('contacts.details')}</h3>
          <dl>
            {#each selected.emails as e (e.value)}
              <dt>{e.label || tk(`contacts.type.${e.type || 'other'}`)} · {t('contacts.field.email')}</dt>
              <dd><button class="link" onclick={() => writeMail(e.value, selected!.displayName)}>{e.value}</button>{#if e.pref}&nbsp;★{/if}</dd>
            {/each}
            {#each selected.phones as p (p.value)}
              <dt>{p.label || tk(`contacts.type.${p.type || 'other'}`)} · {t('contacts.field.phone')}</dt>
              <dd><a href="tel:{p.value.replace(/[^\d+]/g, '')}">{p.value}</a></dd>
            {/each}
            {#each selected.impp as m (m.value)}
              {@const ch = imppChannel(m.value)}
              <dt>{t('contacts.field.impp')}</dt>
              <dd class="ich">{#if ch}<ChannelIcon channel={ch} size={13} />{/if}{m.value.replace(/^[a-z]+:/i, '')}</dd>
            {/each}
            {#each selected.addresses as a, i (i)}
              <dt>{a.label || tk(`contacts.type.${a.type || 'other'}`)} · {t('contacts.field.address')}</dt>
              <dd><a href="https://www.openstreetmap.org/search?query={encodeURIComponent(addressText(a))}" target="_blank" rel="noopener noreferrer"><MapPin size={12} /> {addressText(a)}</a></dd>
            {/each}
            {#each [...selected.urls, ...selected.social] as u (u.value)}
              <dt>{t('contacts.field.url')}</dt>
              <dd><a href={safeHref(u.value)} target="_blank" rel="noopener noreferrer"><Globe size={12} /> {u.value.replace(/^https?:\/\//, '')}</a></dd>
            {/each}
            {#if selected.birthday}<dt>{t('contacts.field.birthday')}</dt><dd><Cake size={12} /> {birthday(selected)}</dd>{/if}
            {#if selected.anniversary}<dt>{t('contacts.field.anniversary')}</dt><dd>{fmt().dateLong(selected.anniversary + 'T12:00:00Z')}</dd>{/if}
            {#each selected.custom as f (f.name)}<dt>{f.name}</dt><dd>{f.value}</dd>{/each}
            {#if selected.categories.length}
              <dt>{t('contacts.field.categories')}</dt>
              <dd class="tags">{#each selected.categories as g (g)}<span>{g}</span>{/each}</dd>
            {/if}
          </dl>
          {#if selected.note}<p class="note">{selected.note}</p>{/if}
        </section>
        <section class="card">
          <h3>{t('contacts.person')}</h3>
          {#if selected.channels.length}
            <p class="muted">{t('contacts.personHint')}</p>
            <ul class="channels">
              {#each selected.channels as h (h.kind + h.value)}
                {@const ch = channelOf(h.kind)}
                <li>
                  {#if ch}<ChannelIcon channel={ch} size={14} />{:else if h.kind === 'email'}<Mail size={14} />{:else}<Phone size={14} />{/if}
                  <span class="ck">{tk(`contacts.channel.${h.kind}`)}</span>
                  <span>{h.value}</span>
                </li>
              {/each}
            </ul>
          {:else}
            <p class="muted">{t('contacts.noPerson')}</p>
          {/if}
        </section>
      </div>
    {:else}
      <p class="empty">{t('contacts.select')}</p>
    {/if}
  </div>
</section>

{#if editing}
  <Modal title={editing.id ? t('contacts.edit') : t('contacts.add')} onclose={() => (editing = null)} width="720px">
    <form
      onsubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <ContactForm bind:contact={editing} books={books.filter((b) => b.kind === 'local' || b.kind === 'carddav')} />
      {#if error}<p class="err" role="alert">{error}</p>{/if}
      <button type="submit" hidden aria-hidden="true"></button>
    </form>
    {#snippet footer()}
      <button class="btn" onclick={() => (editing = null)}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" disabled={saving} onclick={save}>{t('common.action.save')}</button>
    {/snippet}
  </Modal>
{/if}
{#if confirmDelete && selected}
  <Modal title={t('contacts.deleteTitle')} onclose={() => (confirmDelete = false)} width="400px">
    <p>{t('contacts.deleteConfirm', { name: selected.displayName })}</p>
    {#snippet footer()}
      <button class="btn" onclick={() => (confirmDelete = false)}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary danger" onclick={remove}>{t('common.action.delete')}</button>
    {/snippet}
  </Modal>
{/if}
{#if showMerge}
  <MergeDialog
    onclose={() => (showMerge = false)}
    onmerged={(c) => {
      selectedId = c.id;
      void loadContacts();
      void loadMeta();
    }}
  />
{/if}

<style>
  .cs {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: grid;
    grid-template-columns: 230px 320px 1fr;
    background: var(--tm-bg);
  }
  .side {
    background: var(--tm-sidebar);
    border-right: 1px solid var(--tm-line);
    padding: 16px 12px;
    display: flex;
    flex-direction: column;
    gap: 10px;
    overflow-y: auto;
  }
  h1 {
    margin: 0 0 4px 4px;
    font-size: 20px;
    font-weight: 650;
  }
  .wide {
    width: 100%;
    justify-content: center;
  }
  nav {
    display: flex;
    flex-direction: column;
    gap: 1px;
    flex: 1;
  }
  .sect {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--tm-faint);
    margin: 10px 0 2px 6px;
  }
  .nav-item {
    display: flex;
    align-items: center;
    gap: 8px;
    border: none;
    background: none;
    font: inherit;
    font-size: 13px;
    color: var(--tm-fg);
    padding: 6px 8px;
    border-radius: 7px;
    cursor: pointer;
    text-align: left;
  }
  .nav-item span {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    display: flex;
    flex-direction: column;
  }
  .nav-item em {
    font-style: normal;
    font-size: 11px;
    color: var(--tm-faint);
  }
  .nav-item small {
    color: var(--tm-faint);
  }
  .nav-item:hover {
    background: var(--tm-accent-soft);
  }
  .nav-item[aria-current='true'] {
    background: var(--tm-accent-soft);
    font-weight: 600;
  }
  .nav-item.muted {
    color: var(--tm-muted);
  }
  .newbook .input {
    height: 30px;
  }
  .list {
    border-right: 1px solid var(--tm-line);
    background: var(--tm-surface);
    display: flex;
    flex-direction: column;
    min-height: 0;
  }
  .lhead {
    padding: 12px;
    border-bottom: 1px solid var(--tm-line);
  }
  .list ul {
    list-style: none;
    margin: 0;
    padding: 0 0 24px;
    overflow-y: auto;
    flex: 1;
  }
  .letter {
    position: sticky;
    top: 0;
    z-index: 1;
    padding: 4px 16px;
    font-size: 11.5px;
    font-weight: 700;
    color: var(--tm-muted);
    background: var(--tm-surface-2, var(--tm-bg));
  }
  .row {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    border: none;
    background: none;
    font: inherit;
    text-align: left;
    color: var(--tm-fg);
    cursor: pointer;
  }
  .row:hover {
    background: var(--tm-accent-soft);
  }
  .row[aria-current='true'] {
    background: var(--tm-accent-soft);
    box-shadow: inset 3px 0 0 var(--tm-accent);
  }
  .rinfo {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .rn {
    font-weight: 600;
    font-size: 13.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .rs {
    font-size: 12px;
    color: var(--tm-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chs {
    display: flex;
    gap: 3px;
  }
  .detail {
    min-width: 0;
    overflow-y: auto;
    padding: 24px 28px;
  }
  .empty {
    color: var(--tm-muted);
    text-align: center;
    margin-top: 48px;
  }
  .dhead {
    display: flex;
    gap: 16px;
    align-items: center;
  }
  .back {
    display: none;
  }
  .dn {
    flex: 1;
    min-width: 0;
  }
  h2 {
    margin: 0;
    font-size: 22px;
    font-weight: 650;
  }
  .dn p {
    margin: 2px 0 0;
    color: var(--tm-muted);
    font-size: 13.5px;
  }
  .dn .book {
    font-size: 12px;
    color: var(--tm-faint);
  }
  .dact {
    display: flex;
    gap: 6px;
    align-self: flex-start;
  }
  .quick {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    margin: 16px 0;
  }
  .cards {
    display: grid;
    grid-template-columns: minmax(0, 3fr) minmax(0, 2fr);
    gap: 14px;
    align-items: start;
  }
  .card {
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
    padding: 14px 16px;
  }
  h3 {
    margin: 0 0 10px;
    font-size: 13px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--tm-muted);
  }
  dl {
    margin: 0;
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 8px 16px;
    font-size: 13.5px;
  }
  dt {
    color: var(--tm-muted);
    font-size: 12.5px;
  }
  dd {
    margin: 0;
    min-width: 0;
    word-break: break-word;
  }
  dd a,
  .link {
    color: var(--tm-accent-text);
    text-decoration: none;
  }
  .link {
    border: none;
    background: none;
    font: inherit;
    padding: 0;
    cursor: pointer;
  }
  .ich {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .tags {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }
  .tags span {
    padding: 0 8px;
    border-radius: 99px;
    background: var(--tm-accent-soft);
    font-size: 12px;
  }
  .note {
    white-space: pre-wrap;
    margin: 12px 0 0;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .muted {
    color: var(--tm-muted);
    font-size: 12.5px;
    margin: 0 0 8px;
  }
  .channels {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 13px;
  }
  .channels li {
    display: flex;
    gap: 8px;
    align-items: center;
    min-width: 0;
  }
  .ck {
    color: var(--tm-muted);
    width: 70px;
    flex: none;
  }
  .err {
    color: var(--tm-danger);
  }
  .danger {
    background: var(--tm-danger);
    border-color: var(--tm-danger);
  }
  @media (max-width: 1099px) {
    .cs {
      grid-template-columns: 200px 280px 1fr;
    }
    .cards {
      grid-template-columns: 1fr;
    }
  }
  @media (max-width: 639px) {
    .cs {
      display: flex;
      flex-direction: column;
    }
    .side {
      flex: none;
      border-right: none;
      border-bottom: 1px solid var(--tm-line);
      padding: 10px 12px;
      max-height: 40vh;
    }
    .side nav,
    .side .btn-ghost {
      display: none;
    }
    .list {
      flex: 1;
      border-right: none;
    }
    .hidden-narrow {
      display: none !important;
    }
    .detail {
      flex: 1;
      padding: 14px 14px 80px;
    }
    .back {
      display: inline-grid;
    }
    .dhead {
      flex-wrap: wrap;
    }
  }
</style>
