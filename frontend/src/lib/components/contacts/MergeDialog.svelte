<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Merge from '@lucide/svelte/icons/merge';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { DuplicateGroup, PimContact } from '../../types';
  import Modal from '../common/Modal.svelte';
  import Avatar from '../common/Avatar.svelte';

  interface Props {
    onclose: () => void;
    onmerged: (c: PimContact) => void;
  }
  let { onclose, onmerged }: Props = $props();

  let groups = $state<DuplicateGroup[] | null>(null);
  let busy = $state(false);
  /** Selected members and primary per group. */
  let picked = $state<Record<number, number[]>>({});
  let primary = $state<Record<number, number>>({});

  $effect(() => {
    void app.api.findDuplicateContacts().then((g) => {
      groups = g;
      picked = Object.fromEntries(g.map((x, i) => [i, x.contacts.map((c) => c.id)]));
      primary = Object.fromEntries(g.map((x, i) => [i, x.contacts[0].id]));
    });
  });

  function summary(c: PimContact): string {
    return [c.org, ...c.emails.map((e) => e.value), ...c.phones.map((p) => p.value)].filter(Boolean).join(' · ');
  }

  async function merge(i: number) {
    const g = groups![i];
    const ids = picked[i].filter((id) => g.contacts.some((c) => c.id === id));
    if (ids.length < 2) return;
    const first = primary[i];
    const order = [first, ...ids.filter((x) => x !== first)];
    busy = true;
    try {
      const merged = await app.api.mergeContacts(order, ids.length === g.contacts.length && first === g.contacts[0].id ? g.preview : undefined);
      groups = groups!.filter((_, j) => j !== i);
      app.toast(t('contacts.merged', { name: merged.displayName }));
      onmerged(merged);
    } catch (err) {
      app.toast(err instanceof Error ? err.message : String(err), undefined, 'error');
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={t('contacts.duplicatesTitle')} {onclose} width="680px">
  {#if groups === null}
    <p class="muted">{t('contacts.searching')}</p>
  {:else if groups.length === 0}
    <p class="muted">{t('contacts.noDuplicates')}</p>
  {:else}
    <p class="muted">{t('contacts.mergeHint')}</p>
    {#each groups as g, i (g.contacts.map((c) => c.id).join('-'))}
      <section class="grp">
        <ul>
          {#each g.contacts as c (c.id)}
            <li>
              <input type="checkbox" checked={picked[i]?.includes(c.id)} aria-label={t('contacts.includeInMerge', { name: c.displayName })} onchange={(e) => (picked[i] = (e.currentTarget as HTMLInputElement).checked ? [...picked[i], c.id] : picked[i].filter((x) => x !== c.id))} />
              <Avatar name={c.displayName} seed={c.emails[0]?.value ?? c.id} size={32} url={c.photoUrl} />
              <span class="who"><strong>{c.displayName}</strong><small>{summary(c)}</small></span>
              <label class="prim"><input type="radio" name="prim-{i}" checked={primary[i] === c.id} onchange={() => (primary[i] = c.id)} /> {t('contacts.keepAsMain')}</label>
            </li>
          {/each}
        </ul>
        <div class="preview">
          <span class="label">{t('contacts.mergePreview')}</span>
          <strong>{g.preview.displayName}</strong>
          <small>{summary(g.preview)}</small>
        </div>
        <button class="btn btn-primary" disabled={busy || (picked[i]?.length ?? 0) < 2} onclick={() => merge(i)}><Merge size={15} /> {t('contacts.merge')}</button>
      </section>
    {/each}
  {/if}
  {#snippet footer()}
    <button class="btn" onclick={onclose}>{t('common.action.close')}</button>
  {/snippet}
</Modal>

<style>
  .muted {
    color: var(--tm-muted);
    font-size: 13px;
    margin: 0 0 10px;
  }
  .grp {
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    padding: 10px;
    margin-bottom: 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  li {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .who {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .who small,
  .preview small {
    color: var(--tm-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .prim {
    font-size: 12px;
    color: var(--tm-muted);
    display: flex;
    gap: 4px;
    align-items: center;
  }
  .preview {
    display: flex;
    flex-direction: column;
    padding: 8px 10px;
    border-radius: 8px;
    background: var(--tm-accent-soft);
  }
  .grp .btn {
    align-self: flex-end;
  }
</style>
