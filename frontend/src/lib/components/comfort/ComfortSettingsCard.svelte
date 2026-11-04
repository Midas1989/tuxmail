<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Mail comfort: spam filter, smart categories, newsletters, spell checking, video links, mail merge, PDF. -->
<script lang="ts">
  import Newspaper from '@lucide/svelte/icons/newspaper';
  import X from '@lucide/svelte/icons/x';
  import { comfort } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { CATEGORIES, type ComfortSettings } from '../../api/comfort';
  import SubscriptionsDialog from './SubscriptionsDialog.svelte';

  let s = $state<ComfortSettings | null>(null);
  let words = $state<string[]>([]);
  let newWord = $state('');
  let saved = $state(false);
  let subsOpen = $state(false);

  $effect(() => {
    void comfort.loadSettings().then((v) => v && (s = structuredClone($state.snapshot(v) as ComfortSettings)));
    void comfort.api.spellWords().then((w) => (words = w)).catch(() => {});
  });

  async function save() {
    if (!s) return;
    await comfort.saveSettings($state.snapshot(s) as ComfortSettings);
    saved = true;
    setTimeout(() => (saved = false), 2000);
  }

  async function addWord() {
    const w = newWord.trim();
    if (!w) return;
    await comfort.api.addSpellWord(w);
    words = [...words, w].sort();
    newWord = '';
  }

  async function removeWord(w: string) {
    await comfort.api.deleteSpellWord(w);
    words = words.filter((x) => x !== w);
  }

  function toggleTab(c: (typeof CATEGORIES)[number], on: boolean) {
    if (!s) return;
    const cur = s.categories.tabs?.length ? s.categories.tabs : [...CATEGORIES];
    s.categories.tabs = on ? CATEGORIES.filter((x) => cur.includes(x) || x === c) : cur.filter((x) => x !== c);
  }
</script>

<section class="card wide" id="set-comfort" aria-labelledby="set-comfort-h">
  <div class="head">
    <h2 id="set-comfort-h">{t('comfort.settings.title')}</h2>
    {#if saved}<span class="saved" role="status">{t('notifications.saved')}</span>{/if}
  </div>
  {#if s}
    <div class="grid">
      <fieldset>
        <legend>{t('comfort.settings.spam')}</legend>
        <label class="chk"><input type="checkbox" bind:checked={s.spam.enabled} /> {t('comfort.settings.spamEnabled')}</label>
        <label class="rng">{t('comfort.settings.unsure', { n: s.spam.unsure })}<input type="range" min="10" max="95" bind:value={s.spam.unsure} /></label>
        <label class="rng">{t('comfort.settings.spamThreshold', { n: s.spam.spam })}<input type="range" min="50" max="100" bind:value={s.spam.spam} /></label>
        <label class="chk"><input type="checkbox" bind:checked={s.spam.moveToJunk} /> {t('comfort.settings.moveToJunk')}</label>
        <label class="chk"><input type="checkbox" bind:checked={s.spam.serverLearn} /> {t('comfort.settings.serverLearn')}</label>
      </fieldset>

      <fieldset>
        <legend>{t('comfort.settings.categories')}</legend>
        <label class="chk"><input type="checkbox" bind:checked={s.categories.enabled} /> {t('comfort.settings.categoriesEnabled')}</label>
        <div class="tabs">
          {#each CATEGORIES as c (c)}
            <label class="chk"><input type="checkbox" checked={!s.categories.tabs?.length || s.categories.tabs.includes(c)} onchange={(e) => toggleTab(c, (e.currentTarget as HTMLInputElement).checked)} /> {t(`comfort.category.${c}`)}</label>
          {/each}
        </div>
        <label class="chk"><input type="checkbox" bind:checked={s.categories.aiRefine} /> {t('comfort.settings.aiRefine')}</label>
        <p class="hint">{t('comfort.settings.categoriesHint')}</p>
      </fieldset>

      <fieldset>
        <legend>{t('comfort.settings.newsletter')}</legend>
        <label class="chk"><input type="checkbox" bind:checked={s.newsletter.showBar} /> {t('comfort.settings.newsletterBar')}</label>
        <button class="btn" onclick={() => (subsOpen = true)}><Newspaper size={15} /> {t('comfort.subscriptions.open')}</button>
      </fieldset>

      <fieldset>
        <legend>{t('comfort.settings.spell')}</legend>
        <label class="chk"><input type="checkbox" bind:checked={s.spell.enabled} /> {t('comfort.settings.spellEnabled')}</label>
        <label class="field">
          <span class="label">{t('comfort.spell.language')}</span>
          <select class="input" bind:value={s.spell.language}>
            <option value="auto">{t('comfort.spell.auto')}</option>
            <option value="de">{t('comfort.spell.lang.de')}</option>
            <option value="en">{t('comfort.spell.lang.en')}</option>
          </select>
        </label>
        <span class="label">{t('comfort.settings.userDictionary')}</span>
        <div class="words">
          {#each words as w (w)}
            <span class="word">{w}<button class="x" aria-label={t('comfort.settings.removeWord', { word: w })} onclick={() => removeWord(w)}><X size={11} /></button></span>
          {/each}
        </div>
        <div class="inline">
          <input class="input" bind:value={newWord} placeholder={t('comfort.settings.addWord')} aria-label={t('comfort.settings.addWord')} onkeydown={(e) => e.key === 'Enter' && addWord()} />
          <button class="btn" onclick={addWord}>{t('comfort.add')}</button>
        </div>
      </fieldset>

      <fieldset>
        <legend>{t('comfort.settings.meeting')}</legend>
        <label class="field">
          <span class="label">{t('comfort.meeting.providerLabel')}</span>
          <select class="input" bind:value={s.meeting.defaultProvider}>
            <option value="jitsi">{t('comfort.meeting.provider.jitsi')}</option>
            <option value="nextcloud_talk">{t('comfort.meeting.provider.nextcloud_talk')}</option>
            <option value="teams">{t('comfort.meeting.provider.teams')}</option>
          </select>
        </label>
        <label class="field"><span class="label">{t('comfort.settings.jitsiUrl')}</span><input class="input" bind:value={s.meeting.jitsiBaseUrl} spellcheck="false" /></label>
      </fieldset>

      <fieldset>
        <legend>{t('comfort.settings.mergePdf')}</legend>
        <label class="field"><span class="label">{t('comfort.merge.perMinute')}</span><input class="input" type="number" min="1" max="600" bind:value={s.merge.perMinute} /></label>
        <label class="field"><span class="label">{t('comfort.settings.pdfName')}</span><input class="input" bind:value={s.pdf.nameTemplate} spellcheck="false" /></label>
        <label class="chk"><input type="checkbox" bind:checked={s.pdf.useChromium} /> {t('comfort.settings.useChromium')}</label>
        <label class="field"><span class="label">{t('comfort.settings.chromiumPath')}</span><input class="input" bind:value={s.pdf.chromiumPath} spellcheck="false" placeholder={t('comfort.settings.chromiumAuto')} /></label>
      </fieldset>
    </div>
    <div class="foot"><button class="btn btn-primary" onclick={save}>{t('common.action.save')}</button></div>
  {/if}
</section>
{#if subsOpen}<SubscriptionsDialog onclose={() => (subsOpen = false)} />{/if}

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .wide {
    grid-column: 1 / -1;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h2 {
    margin: 0 0 12px;
    font-size: 15px;
  }
  .saved {
    color: #16a34a;
    font-size: 12.5px;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 14px;
  }
  fieldset {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  legend {
    font-weight: 600;
    font-size: 13px;
    padding: 0 4px;
  }
  .chk {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 13px;
  }
  .rng {
    display: flex;
    flex-direction: column;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .tabs {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 2px 8px;
  }
  .hint {
    margin: 0;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .words {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .word {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    padding: 1px 4px 1px 8px;
    border-radius: 999px;
    background: color-mix(in srgb, var(--tm-accent) 12%, transparent);
    font-size: 12px;
  }
  .x {
    border: none;
    background: none;
    padding: 2px;
    display: inline-flex;
    cursor: pointer;
    color: var(--tm-muted);
  }
  .inline {
    display: flex;
    gap: 6px;
  }
  .foot {
    display: flex;
    justify-content: flex-end;
    margin-top: 12px;
  }
</style>
