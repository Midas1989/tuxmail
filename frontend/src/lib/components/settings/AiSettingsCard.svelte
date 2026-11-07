<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> AI: provider, model, key, access per source (AiAccessList), privacy. -->
<script lang="ts">
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import AiAccessList from './AiAccessList.svelte';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { AiProvider, AiSettings, AiSourceAccess } from '../../types';

  const PROVIDERS: { id: AiProvider; url: string; model: string; local?: boolean }[] = [
    { id: 'none', url: '', model: '' },
    { id: 'ollama', url: 'http://localhost:11434', model: 'llama3.1:8b', local: true },
    { id: 'openai', url: 'https://api.openai.com/v1', model: 'gpt-4o-mini' },
    { id: 'gemini', url: 'https://generativelanguage.googleapis.com/v1beta', model: 'gemini-1.5-flash' },
    { id: 'deepseek', url: 'https://api.deepseek.com/v1', model: 'deepseek-chat' },
    { id: 'anthropic', url: 'https://api.anthropic.com/v1', model: 'claude-3-5-haiku-latest' },
    { id: 'mistral', url: 'https://api.mistral.ai/v1', model: 'mistral-small-latest' },
    { id: 'custom', url: 'http://localhost:8000/v1', model: '' },
  ];

  let s = $state<AiSettings>({ provider: 'none', baseUrl: '', model: '', apiKeySet: false, accountPermissions: {}, maskPersonalData: true, sources: {}, accessVersion: 2 });
  let sources = $state<Record<string, AiSourceAccess>>({});
  let apiKey = $state('');
  let saved = $state(false);
  $effect(() => {
    if (app.aiSettings) {
      const v = structuredClone($state.snapshot(app.aiSettings) as AiSettings);
      sources = v.sources ?? {};
      s = v;
    }
    else void app.api.getAiSettings().then((v) => (app.aiSettings ??= v));
  });
  const def = $derived(PROVIDERS.find((p) => p.id === s.provider) ?? PROVIDERS[0]);
  // The core reports whether the stored provider is local; while editing, judge by the chosen one.
  const local = $derived(s.provider === app.aiSettings?.provider && s.baseUrl === app.aiSettings?.baseUrl ? !!app.aiSettings?.local : !!def.local);

  function onProvider() {
    const p = PROVIDERS.find((x) => x.id === s.provider)!;
    s.baseUrl = p.url;
    s.model = p.model;
    apiKey = '';
  }

  async function save() {
    const { catalog: _c, local: _l, maxLevel: _m, ...rest } = $state.snapshot(s) as AiSettings;
    await app.saveAiSettings({ ...rest, sources: $state.snapshot(sources), accessVersion: 2, apiKey: apiKey || undefined });
    apiKey = '';
    saved = true;
    setTimeout(() => (saved = false), 2000);
  }
</script>

<section class="card" id="set-ai" aria-labelledby="set-ai-h">
  <div class="head">
    <h2 id="set-ai-h">{t('settings.ai.title')}</h2>
    {#if saved}<span class="saved" role="status">{t('notifications.saved')}</span>{/if}
  </div>
  <p class="hint">{t('settings.ai.intro')}</p>
  <div class="grid">
    <label class="field">
      <span class="label">{t('settings.ai.provider')}</span>
      <select class="input" bind:value={s.provider} onchange={onProvider}>
        {#each PROVIDERS as p (p.id)}<option value={p.id}>{t(`settings.ai.providers.${p.id}`)}</option>{/each}
      </select>
    </label>
    {#if s.provider !== 'none'}
      <label class="field">
        <span class="label">{t('settings.ai.model')}</span>
        <input class="input" bind:value={s.model} placeholder={def.model} />
      </label>
      <label class="field wide">
        <span class="label">{t('settings.ai.baseUrl')}</span>
        <input class="input" bind:value={s.baseUrl} placeholder={def.url} spellcheck="false" />
      </label>
      {#if !def.local}
        <label class="field wide">
          <span class="label">{t('settings.ai.apiKey')}</span>
          <input class="input" type="password" bind:value={apiKey} autocomplete="off" placeholder={s.apiKeySet ? t('settings.ai.keySaved') : ''} />
        </label>
      {/if}
    {/if}
  </div>
  {#if s.provider !== 'none'}
    <p class="note" class:warn={!local}>
      {#if local}<ShieldCheck size={14} /> {t('settings.ai.access.local')}
      {:else}<TriangleAlert size={14} /> {t('settings.ai.access.external', { provider: t(`settings.ai.providers.${s.provider}`) })}{/if}
    </p>
    <AiAccessList bind:sources catalog={s.catalog ?? []} maxLevel={s.maxLevel} />
    <label class="chk"><input type="checkbox" bind:checked={s.maskPersonalData} /> {t('settings.ai.mask')}</label>
  {/if}
  <p class="note"><ShieldCheck size={14} /> {t('settings.ai.pgpNote')}</p>
  <button class="btn btn-primary" onclick={save}>{t('chat.folders.save')}</button>
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  .saved {
    font-size: 12px;
    color: var(--tm-success);
  }
  .hint {
    margin: 4px 0 12px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px 14px;
    margin-bottom: 12px;
  }
  .field {
    display: flex;
    flex-direction: column;
  }
  .wide {
    grid-column: span 2;
  }
  .chk {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13.5px;
    padding: 3px 0;
  }
  .chk input {
    accent-color: var(--tm-accent);
  }
  .note {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 10px 0 12px;
    font-size: 12px;
    color: var(--tm-success);
  }
  .note.warn {
    color: var(--tm-warn);
    align-items: flex-start;
  }
  .note :global(svg) {
    flex-shrink: 0;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .grid {
      grid-template-columns: 1fr;
    }
    .wide {
      grid-column: auto;
    }
  }
</style>
