<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts" module>
  import type { PimContact } from '../../types';

  export function blankContact(addressBookId = 0): PimContact {
    return {
      id: 0,
      addressBookId,
      displayName: '',
      hasPhoto: false,
      channels: [],
      uid: '',
      fn: '',
      n: {},
      emails: [{ value: '', type: 'home' }],
      phones: [{ value: '', type: 'cell' }],
      addresses: [],
      urls: [],
      impp: [],
      social: [],
      categories: [],
      custom: [],
    };
  }

  /** Drop empty rows before saving. */
  export function cleanContact(c: PimContact): PimContact {
    const ne = <T extends { value: string }>(l: T[]) => l.filter((x) => x.value.trim());
    return {
      ...c,
      emails: ne(c.emails),
      phones: ne(c.phones),
      urls: ne(c.urls),
      impp: ne(c.impp),
      social: ne(c.social),
      addresses: c.addresses.filter((a) => [a.street, a.locality, a.postalCode, a.country, a.region].some((v) => v?.trim())),
      custom: c.custom.filter((f) => f.name.trim()),
      categories: c.categories.map((g) => g.trim()).filter(Boolean),
    };
  }
</script>

<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import X from '@lucide/svelte/icons/x';
  import Camera from '@lucide/svelte/icons/camera';
  import { t } from '../../i18n/index.svelte';
  import { tk } from '../../calendar/i18n';
  import type { ContactValue, PimAddressBook } from '../../types';
  import Avatar from '../common/Avatar.svelte';

  interface Props {
    contact: PimContact;
    books: PimAddressBook[];
  }
  let { contact = $bindable(), books }: Props = $props();

  type ListKey = 'emails' | 'phones' | 'urls' | 'impp' | 'social';
  const TYPES: Record<ListKey, string[]> = {
    emails: ['home', 'work', 'other'],
    phones: ['cell', 'home', 'work', 'fax', 'other'],
    urls: ['home', 'work', 'other'],
    impp: ['other'],
    social: ['other'],
  };
  const LISTS: { key: ListKey; label: string; input: string; placeholder?: string }[] = [
    { key: 'emails', label: 'contacts.field.email', input: 'email' },
    { key: 'phones', label: 'contacts.field.phone', input: 'tel' },
    { key: 'impp', label: 'contacts.field.impp', input: 'text', placeholder: 'contacts.field.imppPlaceholder' },
    { key: 'urls', label: 'contacts.field.url', input: 'url' },
    { key: 'social', label: 'contacts.field.social', input: 'url' },
  ];

  let fileInput = $state<HTMLInputElement>();
  let photoPreview = $state<string | undefined>(contact.photoUrl);
  let groupsText = $state(contact.categories.join(', '));
  $effect(() => {
    contact.categories = groupsText.split(',').map((s) => s.trim()).filter(Boolean);
  });

  function add(key: ListKey) {
    contact[key] = [...contact[key], { value: '', type: TYPES[key][0] } as ContactValue];
  }
  function remove(key: ListKey, i: number) {
    contact[key] = contact[key].filter((_, j) => j !== i);
  }

  async function pickPhoto(e: Event) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0];
    if (!f) return;
    // Downscale to 256 px to keep vCards small.
    const url = URL.createObjectURL(f);
    try {
      const img = new Image();
      await new Promise((res, rej) => ((img.onload = res), (img.onerror = rej), (img.src = url)));
      const size = 256;
      const c = document.createElement('canvas');
      const scale = Math.min(1, size / Math.max(img.width, img.height));
      c.width = Math.round(img.width * scale);
      c.height = Math.round(img.height * scale);
      c.getContext('2d')?.drawImage(img, 0, 0, c.width, c.height);
      const data = c.toDataURL('image/jpeg', 0.85);
      contact.photo = data;
      photoPreview = data;
    } finally {
      URL.revokeObjectURL(url);
    }
  }

  function removePhoto() {
    contact.photo = '-';
    photoPreview = undefined;
  }

  const writableBooks = $derived(books);
</script>

<div class="cf">
  <div class="top">
    <div class="photo">
      <Avatar name={contact.fn || contact.n.given || contact.org || '?'} seed={contact.emails[0]?.value} size={72} url={photoPreview} />
      <div class="pbtn">
        <button type="button" class="btn btn-ghost" onclick={() => fileInput?.click()}><Camera size={14} /> {t('contacts.changePhoto')}</button>
        {#if photoPreview}<button type="button" class="btn btn-ghost" onclick={removePhoto}>{t('contacts.removePhoto')}</button>{/if}
      </div>
      <input bind:this={fileInput} type="file" accept="image/*" hidden onchange={pickPhoto} />
    </div>
    <div class="names">
      <div class="grid2">
        <label><span class="label">{t('contacts.field.given')}</span><input class="input" bind:value={contact.n.given} data-autofocus /></label>
        <label><span class="label">{t('contacts.field.family')}</span><input class="input" bind:value={contact.n.family} /></label>
        <label><span class="label">{t('contacts.field.prefix')}</span><input class="input" bind:value={contact.n.prefix} /></label>
        <label><span class="label">{t('contacts.field.nickname')}</span><input class="input" bind:value={contact.nickname} /></label>
      </div>
      <label><span class="label">{t('contacts.field.fn')}</span><input class="input" bind:value={contact.fn} placeholder={t('contacts.field.fnPlaceholder')} /></label>
    </div>
  </div>
  <div class="grid3">
    <label><span class="label">{t('contacts.field.org')}</span><input class="input" bind:value={contact.org} /></label>
    <label><span class="label">{t('contacts.field.department')}</span><input class="input" bind:value={contact.department} /></label>
    <label><span class="label">{t('contacts.field.jobTitle')}</span><input class="input" bind:value={contact.title} /></label>
  </div>

  {#each LISTS as l (l.key)}
    <fieldset>
      <legend class="label">{tk(l.label)}</legend>
      {#each contact[l.key] as v, i (i)}
        <div class="vrow">
          {#if TYPES[l.key].length > 1}
            <select class="input type" bind:value={v.type} aria-label={t('contacts.field.type')}>
              {#each TYPES[l.key] as ty (ty)}<option value={ty}>{tk(`contacts.type.${ty}`)}</option>{/each}
            </select>
          {/if}
          <input class="input" type={l.input} bind:value={v.value} aria-label={tk(l.label)} placeholder={l.placeholder ? tk(l.placeholder) : undefined} />
          <button type="button" class="icon-btn sm" aria-label={t('contacts.removeField')} onclick={() => remove(l.key, i)}><X size={14} /></button>
        </div>
      {/each}
      <button type="button" class="btn btn-ghost add" onclick={() => add(l.key)}><Plus size={14} /> {t('contacts.addField', { field: tk(l.label) })}</button>
    </fieldset>
  {/each}

  <fieldset>
    <legend class="label">{t('contacts.field.address')}</legend>
    {#each contact.addresses as a, i (i)}
      <div class="addr">
        <select class="input type" bind:value={a.type} aria-label={t('contacts.field.type')}>
          {#each ['home', 'work', 'other'] as ty (ty)}<option value={ty}>{tk(`contacts.type.${ty}`)}</option>{/each}
        </select>
        <input class="input street" bind:value={a.street} placeholder={t('contacts.field.street')} aria-label={t('contacts.field.street')} />
        <input class="input zip" bind:value={a.postalCode} placeholder={t('contacts.field.postalCode')} aria-label={t('contacts.field.postalCode')} />
        <input class="input city" bind:value={a.locality} placeholder={t('contacts.field.city')} aria-label={t('contacts.field.city')} />
        <input class="input country" bind:value={a.country} placeholder={t('contacts.field.country')} aria-label={t('contacts.field.country')} />
        <button type="button" class="icon-btn sm" aria-label={t('contacts.removeField')} onclick={() => (contact.addresses = contact.addresses.filter((_, j) => j !== i))}><X size={14} /></button>
      </div>
    {/each}
    <button type="button" class="btn btn-ghost add" onclick={() => (contact.addresses = [...contact.addresses, { type: 'home' }])}><Plus size={14} /> {t('contacts.addField', { field: t('contacts.field.address') })}</button>
  </fieldset>

  <div class="grid3">
    <label><span class="label">{t('contacts.field.birthday')}</span><input class="input" type="date" bind:value={contact.birthday} /></label>
    <label><span class="label">{t('contacts.field.anniversary')}</span><input class="input" type="date" bind:value={contact.anniversary} /></label>
    <label>
      <span class="label">{t('contacts.field.book')}</span>
      <select class="input" bind:value={contact.addressBookId}>
        {#each writableBooks as b (b.id)}<option value={b.id}>{b.name}{b.accountName ? ` (${b.accountName})` : ''}</option>{/each}
      </select>
    </label>
  </div>
  <label><span class="label">{t('contacts.field.categories')}</span><input class="input" bind:value={groupsText} placeholder={t('contacts.field.categoriesPlaceholder')} /></label>

  <fieldset>
    <legend class="label">{t('contacts.field.custom')}</legend>
    {#each contact.custom as f, i (i)}
      <div class="vrow">
        <input class="input type" bind:value={f.name} placeholder={t('contacts.field.customName')} aria-label={t('contacts.field.customName')} />
        <input class="input" bind:value={f.value} aria-label={t('contacts.field.customValue')} />
        <button type="button" class="icon-btn sm" aria-label={t('contacts.removeField')} onclick={() => (contact.custom = contact.custom.filter((_, j) => j !== i))}><X size={14} /></button>
      </div>
    {/each}
    <button type="button" class="btn btn-ghost add" onclick={() => (contact.custom = [...contact.custom, { name: '', value: '' }])}><Plus size={14} /> {t('contacts.addField', { field: t('contacts.field.custom') })}</button>
  </fieldset>
  <label><span class="label">{t('contacts.field.note')}</span><textarea class="input" rows="3" bind:value={contact.note}></textarea></label>
</div>

<style>
  .cf {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .top {
    display: flex;
    gap: 16px;
    align-items: flex-start;
  }
  .photo {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
  }
  .pbtn {
    display: flex;
    flex-direction: column;
  }
  .pbtn .btn {
    height: 26px;
    font-size: 12px;
  }
  .names {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .grid2 {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .grid3 {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 8px;
  }
  fieldset {
    border: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .vrow,
  .addr {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .addr {
    flex-wrap: wrap;
  }
  .type {
    width: 120px;
    flex: none;
  }
  .street {
    flex: 2;
    min-width: 160px;
  }
  .zip {
    width: 90px;
  }
  .city,
  .country {
    flex: 1;
    min-width: 110px;
  }
  .sm {
    width: 28px;
    height: 28px;
    flex: none;
  }
  .add {
    align-self: flex-start;
    height: 28px;
    font-size: 12.5px;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .top {
      flex-direction: column;
      align-items: stretch;
    }
    .grid2,
    .grid3 {
      grid-template-columns: 1fr;
    }
    .type {
      width: 96px;
    }
  }
</style>
