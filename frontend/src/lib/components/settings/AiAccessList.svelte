<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> AI -> "What the assistant may access": one row per account/area with off / metadata / content.
     The core enforces these levels (internal/app/ai_access.go); this list only edits them. -->
<script lang="ts">
  import { t } from '../../i18n/index.svelte';
  import { AI_LEVELS, folderExcluded, minLevel } from '../../aiaccess';
  import type { AiLevel, AiSource, AiSourceAccess, AiSourceKind } from '../../types';

  interface Props {
    sources: Record<string, AiSourceAccess>;
    catalog: AiSource[];
    maxLevel?: AiLevel;
  }
  let { sources = $bindable(), catalog, maxLevel }: Props = $props();

  const GROUPS: AiSourceKind[] = ['mail', 'messenger', 'calendar', 'addressbook', 'cloud', 'projects'];
  const UNUSED = new Set<AiSourceKind>(['calendar', 'addressbook', 'cloud', 'projects']);
  const rank = (l: AiLevel) => AI_LEVELS.indexOf(l);

  const groups = $derived(GROUPS.map((kind) => ({ kind, items: catalog.filter((c) => c.kind === kind) })).filter((g) => g.items.length));

  const levelOf = (key: string): AiLevel => minLevel(sources[key]?.level, maxLevel);
  const nameOf = (src: AiSource) => (src.kind === 'projects' ? t('settings.ai.access.projectsAll') : src.name);

  function setLevel(key: string, level: AiLevel) {
    sources[key] = { ...sources[key], level };
  }

  function toggleFolder(key: string, id: number, include: boolean) {
    const cur = sources[key] ?? { level: 'off' };
    const ex = new Set(cur.excludeFolders ?? []);
    if (include) ex.delete(id);
    else ex.add(id);
    sources[key] = { ...cur, excludeFolders: [...ex] };
  }

  function allOff() {
    for (const c of catalog) sources[c.key] = { ...sources[c.key], level: 'off' };
  }
</script>

<section class="access" aria-labelledby="ai-access-h">
  <div class="a-head">
    <h3 id="ai-access-h">{t('settings.ai.access.title')}</h3>
    <button class="btn btn-ghost small" type="button" onclick={allOff}>{t('settings.ai.access.allOff')}</button>
  </div>
  <p class="hint">{t('settings.ai.access.intro')}</p>
  {#if maxLevel && maxLevel !== 'content'}
    <p class="hint cap">{t('settings.ai.access.capped', { level: t(`settings.ai.levels.${maxLevel}`) })}</p>
  {/if}
  {#each groups as g (g.kind)}
    <div class="group" role="group" aria-labelledby="ai-g-{g.kind}">
      <h4 id="ai-g-{g.kind}">{t(`settings.ai.kinds.${g.kind}`)}</h4>
      {#if UNUSED.has(g.kind)}<p class="hint small-note">{t('settings.ai.access.notUsed')}</p>{/if}
      {#each g.items as src (src.key)}
        {@const cur = levelOf(src.key)}
        <div class="row">
          <div class="name">
            <span>{nameOf(src)}</span>
            {#if src.detail}<small>{src.detail}</small>{/if}
          </div>
          <div class="seg" role="radiogroup" aria-label={t('settings.ai.access.levelOf', { name: nameOf(src) })}>
            {#each AI_LEVELS as l (l)}
              <label class="opt" class:on={cur === l} class:disabled={!!maxLevel && rank(l) > rank(maxLevel)}>
                <input
                  type="radio"
                  name="ai-{src.key}"
                  value={l}
                  checked={cur === l}
                  disabled={!!maxLevel && rank(l) > rank(maxLevel)}
                  onchange={() => setLevel(src.key, l)}
                />
                {t(`settings.ai.levels.${l}`)}
              </label>
            {/each}
          </div>
        </div>
        {#if src.kind === 'mail' && cur !== 'off' && src.folders?.length}
          {@const acc = sources[src.key]}
          {@const excluded = src.folders.filter((f) => folderExcluded(acc, f.id, f.role)).length}
          <details class="folders">
            <summary>{excluded ? t('settings.ai.access.foldersExcluded', { count: excluded }) : t('settings.ai.access.folders')}</summary>
            <label class="chk">
              <input
                type="checkbox"
                checked={!!acc?.allowJunkTrash}
                onchange={(e) => (sources[src.key] = { ...acc!, allowJunkTrash: (e.currentTarget as HTMLInputElement).checked })}
              />
              {t('settings.ai.access.junkTrash')}
            </label>
            <div class="flist">
              {#each src.folders as f (f.id)}
                <label class="chk">
                  <input
                    type="checkbox"
                    checked={!folderExcluded(acc, f.id, f.role)}
                    disabled={!acc?.allowJunkTrash && (f.role === 'junk' || f.role === 'trash')}
                    aria-label={t('settings.ai.access.include', { name: f.name })}
                    onchange={(e) => toggleFolder(src.key, f.id, (e.currentTarget as HTMLInputElement).checked)}
                  />
                  {f.name}
                </label>
              {/each}
            </div>
          </details>
        {/if}
      {/each}
    </div>
  {/each}
</section>

<style>
  .access {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 10px 12px;
    margin: 0 0 12px;
  }
  .a-head {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 8px;
  }
  h3 {
    margin: 0;
    font-size: 13.5px;
    font-weight: 650;
  }
  h4 {
    margin: 10px 0 4px;
    font-size: 12px;
    font-weight: 600;
    color: var(--tm-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .hint {
    margin: 4px 0 6px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .small-note {
    font-size: 11.5px;
    margin-top: 0;
  }
  .cap {
    color: var(--tm-warn);
  }
  .small {
    font-size: 12px;
    padding: 3px 8px;
  }
  .row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding: 5px 0;
    border-top: 1px solid var(--tm-line);
  }
  .name {
    display: flex;
    flex-direction: column;
    min-width: 0;
    font-size: 13.5px;
  }
  .name small {
    font-size: 11.5px;
    color: var(--tm-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .seg {
    display: inline-flex;
    flex-shrink: 0;
    border: 1px solid var(--tm-line);
    border-radius: 8px;
    overflow: hidden;
  }
  .opt {
    position: relative;
    display: inline-flex;
    align-items: center;
    padding: 5px 10px;
    font-size: 12.5px;
    cursor: pointer;
    user-select: none;
  }
  .opt + .opt {
    border-left: 1px solid var(--tm-line);
  }
  .opt input {
    position: absolute;
    opacity: 0;
    inset: 0;
    margin: 0;
    cursor: pointer;
  }
  .opt.on {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
    font-weight: 600;
  }
  .opt.disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  .opt:focus-within {
    outline: 2px solid var(--tm-accent);
    outline-offset: -2px;
  }
  .folders {
    margin: 0 0 6px 8px;
    font-size: 12.5px;
  }
  .folders summary {
    cursor: pointer;
    color: var(--tm-muted);
    padding: 2px 0;
  }
  .flist {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
    gap: 0 12px;
  }
  .chk {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
    padding: 3px 0;
  }
  .chk input {
    accent-color: var(--tm-accent);
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .row {
      flex-direction: column;
      align-items: stretch;
      gap: 6px;
    }
    .seg {
      display: flex;
    }
    .opt {
      flex: 1;
      justify-content: center;
      padding: 9px 6px;
      font-size: 13px;
    }
  }
</style>
