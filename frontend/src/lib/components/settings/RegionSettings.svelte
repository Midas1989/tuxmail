<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Language & region. Independent of the UI language. -->
<script lang="ts">
  import { prefs } from '../../state';
  import { fmt, i18n, t } from '../../i18n/index.svelte';
  import { DATE_FORMATS, NUMBER_FORMATS, Formatter, listTimeZones, systemTimeZone } from '../../format';
  import type { RegionSettings } from '../../types';

  const zones = listTimeZones();
  let tzFilter = $state('');
  const filteredZones = $derived(zones.filter((z) => z.toLowerCase().includes(tzFilter.toLowerCase().replace(/\s+/g, '_'))).slice(0, 400));
  const sysTz = systemTimeZone();
  const r = $derived(prefs.region);

  // Sample: 3 March 2026, 14:05:09 local in the selected zone (fixed instant for a stable preview).
  const sample = new Date('2026-03-03T13:05:09Z');
  const preview = $derived.by(() => {
    const f = fmt();
    return `${f.full(sample)} · ${f.number(1234567.891, { maximumFractionDigits: 2 })} · ${f.size(24_300_000)}`;
  });
  const relPreview = $derived(fmt().listDate(new Date(Date.now() - 5 * 60_000)));

  function exampleFor(df: RegionSettings['dateFormat']): string {
    if (df === 'locale' || df === 'custom') return '';
    return new Formatter(i18n.locale, { ...r, dateFormat: df }).date(sample);
  }
  function numberExample(nf: RegionSettings['numberFormat']): string {
    return new Formatter(i18n.locale, { ...r, numberFormat: nf }).number(1234.56, { minimumFractionDigits: 2 });
  }

  function set<K extends keyof RegionSettings>(k: K, v: RegionSettings[K]) {
    prefs.setRegion({ [k]: v } as Partial<RegionSettings>);
  }
</script>

<section class="card" aria-labelledby="set-region">
  <h2 id="set-region">{t('settings.region.title')}</h2>

  <div class="grid">
    <label class="field">
      <span class="label">{t('settings.language')}</span>
      <select class="input" value={prefs.locale} onchange={(e) => prefs.setLocale((e.currentTarget as HTMLSelectElement).value)}>
        <option value="system">{t('settings.region.systemLanguage')}</option>
        {#each i18n.locales as l (l.code)}
          <option value={l.code}>{l.nativeName}</option>
        {/each}
      </select>
    </label>

    <div class="field wide">
      <span class="label" id="tz-label">{t('settings.region.timeZone')}</span>
      <label class="check">
        <input type="checkbox" checked={r.timeZone === 'system'} onchange={(e) => set('timeZone', (e.currentTarget as HTMLInputElement).checked ? 'system' : sysTz)} />
        {t('settings.region.useSystemTz')} ({sysTz})
      </label>
      {#if r.timeZone !== 'system'}
        <div class="tz">
          <input class="input" placeholder={t('settings.region.tzSearch')} bind:value={tzFilter} aria-label={t('settings.region.tzSearch')} />
          <select class="input" size="5" aria-labelledby="tz-label" value={r.timeZone} onchange={(e) => set('timeZone', (e.currentTarget as HTMLSelectElement).value)}>
            {#each filteredZones as z (z)}<option value={z}>{z.replace(/_/g, ' ')}</option>{/each}
          </select>
        </div>
      {/if}
    </div>

    <label class="field">
      <span class="label">{t('settings.region.dateFormat')}</span>
      <select class="input" value={r.dateFormat} onchange={(e) => set('dateFormat', (e.currentTarget as HTMLSelectElement).value as RegionSettings['dateFormat'])}>
        {#each DATE_FORMATS as df (df)}
          <option value={df}>
            {df === 'locale' ? t('settings.region.localeDefault') : df === 'custom' ? t('settings.region.custom') : `${df}  (${exampleFor(df)})`}
          </option>
        {/each}
      </select>
      {#if r.dateFormat === 'custom'}
        <input class="input mono" value={r.customDatePattern} oninput={(e) => set('customDatePattern', (e.currentTarget as HTMLInputElement).value)} aria-label={t('settings.region.custom')} />
        <small>{t('settings.region.customHint')}</small>
      {/if}
    </label>

    <label class="field">
      <span class="label">{t('settings.region.timeFormat')}</span>
      <select class="input" value={r.timeFormat} onchange={(e) => set('timeFormat', (e.currentTarget as HTMLSelectElement).value as RegionSettings['timeFormat'])}>
        <option value="locale">{t('settings.region.localeDefault')}</option>
        <option value="24">{t('settings.region.h24')}</option>
        <option value="12">{t('settings.region.h12')}</option>
      </select>
    </label>

    <label class="field">
      <span class="label">{t('settings.region.weekStart')}</span>
      <select class="input" value={String(r.weekStart)} onchange={(e) => {
        const v = (e.currentTarget as HTMLSelectElement).value;
        set('weekStart', v === 'locale' ? 'locale' : (Number(v) as 0 | 1 | 6));
      }}>
        <option value="locale">{t('settings.region.localeDefault')}</option>
        <option value="1">{t('settings.region.monday')}</option>
        <option value="0">{t('settings.region.sunday')}</option>
        <option value="6">{t('settings.region.saturday')}</option>
      </select>
    </label>

    <label class="field">
      <span class="label">{t('settings.region.numberFormat')}</span>
      <select class="input" value={r.numberFormat} onchange={(e) => set('numberFormat', (e.currentTarget as HTMLSelectElement).value as RegionSettings['numberFormat'])}>
        {#each NUMBER_FORMATS as nf (nf)}
          <option value={nf}>{nf === 'locale' ? `${t('settings.region.localeDefault')} (${numberExample('locale')})` : nf}</option>
        {/each}
      </select>
    </label>

    <label class="field">
      <span class="label">{t('settings.region.secondaryTz')}</span>
      <select class="input" value={r.secondaryTimeZone} onchange={(e) => set('secondaryTimeZone', (e.currentTarget as HTMLSelectElement).value)}>
        <option value="">{t('settings.region.none')}</option>
        {#each zones as z (z)}<option value={z}>{z.replace(/_/g, ' ')}</option>{/each}
      </select>
    </label>

    <label class="check field">
      <input type="checkbox" checked={r.relativeTimes} onchange={(e) => set('relativeTimes', (e.currentTarget as HTMLInputElement).checked)} />
      {t('settings.region.relative')}
    </label>
  </div>

  <div class="preview" aria-live="polite">
    <span class="label">{t('settings.region.preview')}</span>
    <output>{preview}</output>
    <output class="rel">{relPreview}</output>
  </div>
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  h2 {
    margin: 0 0 12px;
    font-size: 15px;
    font-weight: 650;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px 16px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .wide {
    grid-column: span 2;
  }
  .check {
    flex-direction: row;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .check input {
    accent-color: var(--tm-accent);
  }
  .tz {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .tz select {
    height: auto;
  }
  .mono {
    font-family: var(--tm-mono);
  }
  small {
    color: var(--tm-faint);
    font-size: 11.5px;
  }
  .preview {
    margin-top: 14px;
    padding: 10px 12px;
    border-radius: 10px;
    background: var(--tm-surface-2);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  output {
    font-family: var(--tm-mono);
    font-size: 13px;
  }
  .rel {
    color: var(--tm-muted);
  }
  @media (max-width: 639px) {
    .grid {
      grid-template-columns: 1fr;
    }
    .wide {
      grid-column: auto;
    }
  }
</style>
