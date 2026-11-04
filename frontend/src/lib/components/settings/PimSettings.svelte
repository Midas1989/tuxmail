<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Kalender & Kontakte: calendars (local, ICS/webcal subscriptions,
  CalDAV, birthdays; the same list as in the calendar sidebar) and address
  books.
-->
<script lang="ts">
  import Users from '@lucide/svelte/icons/users';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { PimAddressBook, PimCalendar } from '../../types';
  import CalendarSidebar from '../calendar/CalendarSidebar.svelte';

  interface Props {
    part: 'calendars' | 'contacts';
  }
  let { part }: Props = $props();

  let calendars = $state<PimCalendar[]>([]);
  let books = $state<PimAddressBook[]>([]);

  async function load() {
    try {
      if (part === 'calendars') calendars = await app.api.listCalendars();
      else books = await app.api.listAddressBooks();
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    }
  }
  $effect(() => {
    void load();
  });
</script>

{#if part === 'calendars'}
  <section class="card" id="set-calendars" aria-labelledby="set-calendars-h">
    <h2 id="set-calendars-h">{t('calendar.calendars')}</h2>
    <p class="intro">{t('settings.pages.pim.calendarsIntro')}</p>
    <div class="cal">
      <CalendarSidebar {calendars} onchanged={load} />
    </div>
  </section>
{:else}
  <section class="card" id="set-books" aria-labelledby="set-books-h">
    <div class="card-head">
      <h2 id="set-books-h">{t('contacts.books')}</h2>
      <button class="btn" onclick={() => app.navigate('contacts')}><Users size={14} /> {t('settings.pages.pim.openContacts')}</button>
    </div>
    <p class="intro">{t('settings.pages.pim.booksIntro')}</p>
    {#if books.length}
      <ul class="rows">
        {#each books as b (b.id)}
          <li>
            <span class="nm">{b.name}{#if b.accountName}<span class="sub">{' · '}{b.accountName}</span>{/if}</span>
            <span class="sub">{t('settings.pages.pim.contactsCount', { count: b.count })}</span>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="sub">{t('settings.pages.pim.booksEmpty')}</p>
    {/if}
  </section>
{/if}

<style>
  .cal {
    max-width: 420px;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 8px;
  }
  .card-head h2 {
    margin: 0;
  }
  .rows {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .rows li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 0;
    border-top: 1px solid var(--tm-line);
  }
  .nm {
    flex: 1;
    min-width: 0;
    font-weight: 550;
  }
  .sub {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
</style>
