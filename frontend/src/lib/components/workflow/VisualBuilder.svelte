<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Visual workflow builder: trigger card -> AND/OR condition groups -> action cards. -->
<script lang="ts" module>
  import type { WorkflowRule, WorkflowSchema } from '../../types';

  export type Cond = { kind: 'row'; field: string; op: string; value: string } | { kind: 'group'; match: 'all' | 'any'; items: Cond[] };

  const NEG: Record<string, string> = { contains: 'not_contains', eq: 'ne', in: 'not_in', is_true: 'is_false', empty: 'not_empty' };

  /** Rule -> editable group (null when the rule uses constructs the builder cannot show). */
  export function ruleToCond(r: WorkflowRule): Cond | null {
    if (r.all || r.any) {
      const items = (r.all ?? r.any)!.map(ruleToCond);
      if (items.some((x) => x === null)) return null;
      return { kind: 'group', match: r.all ? 'all' : 'any', items: items as Cond[] };
    }
    if (r.not) {
      const inner = r.not;
      if (inner.field && inner.op && NEG[inner.op]) return { kind: 'row', field: inner.field, op: NEG[inner.op], value: valueText(inner.value) };
      return null;
    }
    return { kind: 'row', field: r.field ?? '', op: r.op ?? 'eq', value: valueText(r.value) };
  }

  function valueText(v: WorkflowRule['value']): string {
    if (v === undefined) return '';
    return Array.isArray(v) ? v.join(', ') : String(v);
  }

  export function condToRule(c: Cond, schema: WorkflowSchema, root = true): WorkflowRule | undefined {
    if (c.kind === 'group') {
      const items = c.items.map((x) => condToRule(x, schema, false)).filter((x): x is WorkflowRule => !!x);
      if (!items.length) return undefined;
      if (root && items.length === 1) return items[0];
      return c.match === 'all' ? { all: items } : { any: items };
    }
    if (!c.field) return undefined;
    const kind = schema.fields.find((f) => f.name === c.field)?.kind ?? 'string';
    const r: WorkflowRule = { field: c.field, op: c.op };
    if (['is_true', 'is_false', 'empty', 'not_empty'].includes(c.op)) return r;
    if (c.op === 'in' || c.op === 'not_in') r.value = c.value.split(',').map((x) => x.trim()).filter(Boolean);
    else if (kind === 'number' || c.op === 'count_gt' || c.op === 'count_lt') r.value = Number(c.value);
    else if (kind === 'bool') r.value = c.value === 'true';
    else r.value = c.value;
    return r;
  }
</script>

<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import ArrowUp from '@lucide/svelte/icons/arrow-up';
  import ArrowDown from '@lucide/svelte/icons/arrow-down';
  import Zap from '@lucide/svelte/icons/zap';
  import GitBranch from '@lucide/svelte/icons/git-branch';
  import Play from '@lucide/svelte/icons/play';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import X from '@lucide/svelte/icons/x';
  import type { Workflow, WorkflowAction, WorkflowParamSpec } from '../../types';
  import type { WorkflowContext } from '../../workflow/schema';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';

  interface Props {
    workflow: Workflow;
    schema: WorkflowSchema;
    ctx: WorkflowContext;
  }
  let { workflow = $bindable(), schema, ctx }: Props = $props();

  const uid = 'vb-' + Math.random().toString(36).slice(2, 6);
  /** Syntax examples (not translated). */
  const EX = { folders: 'inbox', schedule: 'daily-0800', actionIf: 'vars.count > 1' };
  // svelte-ignore state_referenced_locally
  let condMode = $state<'none' | 'rule' | 'expr'>(workflow.condition?.expr ? 'expr' : workflow.condition?.rule ? 'rule' : 'none');
  // svelte-ignore state_referenced_locally
  const initial = workflow.condition?.rule ? ruleToCond(workflow.condition.rule) : null;
  // svelte-ignore state_referenced_locally
  let unsupported = $state(!!workflow.condition?.rule && !initial);
  let root = $state<Cond>(initial ? (initial.kind === 'group' ? initial : { kind: 'group', match: 'all', items: [initial] }) : { kind: 'group', match: 'all', items: [] });
  // svelte-ignore state_referenced_locally
  let expr = $state(workflow.condition?.expr ?? '');

  const trigSpec = $derived(schema.triggers.find((x) => x.event === workflow.trigger.event));
  const groups = $derived([...new Set(schema.triggers.map((x) => x.group))]);
  const actionGroups = $derived([...new Set(schema.actions.map((x) => x.group))]);

  function syncCondition() {
    if (condMode === 'none') workflow.condition = undefined;
    else if (condMode === 'expr') workflow.condition = expr.trim() ? { expr } : undefined;
    else if (!unsupported) {
      const r = condToRule($state.snapshot(root) as Cond, schema);
      workflow.condition = r ? { rule: r } : undefined;
    }
  }

  function fieldKind(name: string) {
    return schema.fields.find((f) => f.name === name)?.kind ?? 'string';
  }

  function addRow(g: Cond & { kind: 'group' }) {
    g.items.push({ kind: 'row', field: 'mail.subject', op: 'contains', value: '' });
    syncCondition();
  }
  function addGroup(g: Cond & { kind: 'group' }) {
    g.items.push({ kind: 'group', match: g.match === 'all' ? 'any' : 'all', items: [{ kind: 'row', field: 'mail.from', op: 'contains', value: '' }] });
    syncCondition();
  }
  function removeAt(g: Cond & { kind: 'group' }, i: number) {
    g.items.splice(i, 1);
    syncCondition();
  }

  function setEvent(ev: string) {
    const spec = schema.triggers.find((x) => x.event === ev);
    const tr = { ...workflow.trigger, event: ev };
    for (const f of ['accounts', 'folders', 'channels', 'schedule', 'name'] as const) if (!spec?.filters.includes(f)) delete tr[f];
    workflow.trigger = tr;
  }

  function toggleIn<T>(list: T[] | undefined, v: T): T[] | undefined {
    const l = list ?? [];
    const next = l.includes(v) ? l.filter((x) => x !== v) : [...l, v];
    return next.length ? next : undefined;
  }

  function addAction() {
    workflow.actions = [...workflow.actions, { type: 'tag', params: { tags: [] } }];
  }
  function moveAction(i: number, d: -1 | 1) {
    const a = [...workflow.actions];
    const [x] = a.splice(i, 1);
    a.splice(i + d, 0, x);
    workflow.actions = a;
  }
  function setType(a: WorkflowAction, type: string) {
    a.type = type;
    const spec = schema.actions.find((x) => x.type === type);
    const params: Record<string, unknown> = {};
    for (const p of spec?.params ?? []) if (a.params[p.name] !== undefined) params[p.name] = a.params[p.name];
    a.params = params;
  }
  function paramText(a: WorkflowAction, p: WorkflowParamSpec): string {
    const v = a.params[p.name];
    if (v === undefined) return '';
    return Array.isArray(v) ? v.join(', ') : String(v);
  }
  function setParam(a: WorkflowAction, p: WorkflowParamSpec, raw: string | boolean) {
    if (typeof raw === 'boolean') a.params[p.name] = raw || undefined;
    else if (raw === '') delete a.params[p.name];
    else if (p.kind === 'list' || p.kind === 'tag') a.params[p.name] = raw.split(',').map((x) => x.trim()).filter(Boolean);
    else if (p.kind === 'number') a.params[p.name] = Number(raw);
    else a.params[p.name] = raw;
  }
  const datalistFor = (kind: string) => (kind === 'folder' ? uid + '-folders' : kind === 'tag' ? uid + '-tags' : kind === 'project' ? uid + '-projects' : kind === 'template' ? uid + '-templates' : undefined);
</script>

<datalist id="{uid}-folders">{#each ctx.folders as f (f)}<option value={f}></option>{/each}</datalist>
<datalist id="{uid}-tags">{#each ctx.tags as f (f)}<option value={f}></option>{/each}</datalist>
<datalist id="{uid}-projects">{#each ctx.projects as f (f)}<option value={f}></option>{/each}</datalist>
<datalist id="{uid}-templates">{#each ctx.templates as f (f)}<option value={f}></option>{/each}</datalist>

<div class="vb">
  <!-- Trigger -->
  <section class="card trig" aria-labelledby="{uid}-t">
    <h3 id="{uid}-t"><Zap size={15} aria-hidden="true" /> {t('workflow.visual.trigger')}</h3>
    <label class="field">
      <span class="label">{t('workflow.visual.event')}</span>
      <select class="input" value={workflow.trigger.event} onchange={(e) => setEvent((e.currentTarget as HTMLSelectElement).value)}>
        {#each groups as g (g)}
          <optgroup label={t(`workflow.group.${g}`)}>
            {#each schema.triggers.filter((x) => x.group === g) as tr (tr.event)}<option value={tr.event}>{t(`workflow.trigger.${tr.event.replace('.', '_')}` as 'workflow.trigger.manual')}</option>{/each}
          </optgroup>
        {/each}
      </select>
    </label>
    {#if trigSpec?.filters.includes('accounts')}
      <fieldset class="field">
        <legend class="label">{t('workflow.visual.accounts')}</legend>
        <div class="checks">
          {#each app.accounts as acc (acc.id)}
            <label class="chk"><input type="checkbox" checked={workflow.trigger.accounts?.includes(acc.id) ?? false} onchange={() => (workflow.trigger.accounts = toggleIn(workflow.trigger.accounts, acc.id))} /> {acc.displayName}</label>
          {/each}
        </div>
        <small class="hint">{t('workflow.visual.allIfEmpty')}</small>
      </fieldset>
    {/if}
    {#if trigSpec?.filters.includes('folders')}
      <label class="field">
        <span class="label">{t('workflow.visual.folders')}</span>
        <input class="input" list="{uid}-folders" value={(workflow.trigger.folders ?? []).join(', ')} onchange={(e) => { const v = (e.currentTarget as HTMLInputElement).value.split(',').map((x) => x.trim()).filter(Boolean); workflow.trigger.folders = v.length ? v : undefined; }} placeholder={EX.folders} />
      </label>
    {/if}
    {#if trigSpec?.filters.includes('channels')}
      <fieldset class="field">
        <legend class="label">{t('workflow.visual.channels')}</legend>
        <div class="checks">
          {#each ['whatsapp', 'signal', 'telegram'] as ch (ch)}
            <label class="chk"><input type="checkbox" checked={workflow.trigger.channels?.includes(ch) ?? false} onchange={() => (workflow.trigger.channels = toggleIn(workflow.trigger.channels, ch))} /> {t(`common.channel.${ch}` as 'common.channel.signal')}</label>
          {/each}
        </div>
      </fieldset>
    {/if}
    {#if trigSpec?.filters.includes('schedule')}
      <label class="field">
        <span class="label">{t('workflow.visual.schedule')}</span>
        <input class="input" bind:value={workflow.trigger.schedule} placeholder={EX.schedule} />
      </label>
    {/if}
    {#if trigSpec?.filters.includes('name')}
      <label class="field">
        <span class="label">{t('workflow.visual.hookName')}</span>
        <input class="input" bind:value={workflow.trigger.name} />
      </label>
    {/if}
  </section>

  <!-- Condition -->
  <section class="card" aria-labelledby="{uid}-c">
    <div class="card-head">
      <h3 id="{uid}-c"><GitBranch size={15} aria-hidden="true" /> {t('workflow.visual.condition')}</h3>
      <div class="seg sm" role="radiogroup" aria-labelledby="{uid}-c">
        {#each ['none', 'rule', 'expr'] as m (m)}
          <button
            role="radio"
            aria-checked={condMode === m}
            onclick={() => {
              condMode = m as typeof condMode;
              if (m === 'rule') unsupported = false;
              syncCondition();
            }}>{t(`workflow.visual.cond.${m}` as 'workflow.visual.cond.none')}</button
          >
        {/each}
      </div>
    </div>
    {#if condMode === 'none'}
      <p class="hint">{t('workflow.visual.always')}</p>
    {:else if condMode === 'expr'}
      <textarea class="input mono" rows="3" bind:value={expr} oninput={syncCondition} aria-label={t('workflow.visual.cond.expr')} spellcheck="false"></textarea>
      <small class="hint">{t('workflow.visual.exprHint')}</small>
    {:else if unsupported}
      <p class="warn"><TriangleAlert size={14} /> {t('workflow.visual.unsupported')}</p>
    {:else}
      {@render group(root as Cond & { kind: 'group' }, 0)}
    {/if}
  </section>

  <!-- Actions -->
  <section class="card" aria-labelledby="{uid}-a">
    <h3 id="{uid}-a"><Play size={15} aria-hidden="true" /> {t('workflow.visual.actions')}</h3>
    <ol class="actions">
      {#each workflow.actions as a, i (i)}
        {@const spec = schema.actions.find((x) => x.type === a.type)}
        <li class="action">
          <div class="a-head">
            <span class="num">{i + 1}</span>
            <select class="input" value={a.type} onchange={(e) => setType(a, (e.currentTarget as HTMLSelectElement).value)} aria-label={t('workflow.visual.actionType')}>
              {#each actionGroups as g (g)}
                <optgroup label={t(`workflow.actionGroup.${g}`)}>
                  {#each schema.actions.filter((x) => x.group === g) as s (s.type)}
                    <option value={s.type} disabled={s.triggers.length > 0 && !s.triggers.includes(workflow.trigger.event)}>{t(`workflow.action.${s.type}` as 'workflow.action.move')}</option>
                  {/each}
                </optgroup>
              {/each}
            </select>
            {#if spec?.external}<span class="ext" title={t('workflow.externalHint')}>{t('workflow.external')}</span>{/if}
            <span class="grow"></span>
            <button class="icon-btn" disabled={i === 0} aria-label={t('workflow.visual.up')} onclick={() => moveAction(i, -1)}><ArrowUp size={14} /></button>
            <button class="icon-btn" disabled={i === workflow.actions.length - 1} aria-label={t('workflow.visual.down')} onclick={() => moveAction(i, 1)}><ArrowDown size={14} /></button>
            <button class="icon-btn" aria-label={t('common.action.delete')} onclick={() => (workflow.actions = workflow.actions.filter((_, k) => k !== i))}><Trash size={14} /></button>
          </div>
          {#if spec?.params.length}
            <div class="params">
              {#each spec.params as p (p.name)}
                <label class="field" class:wide={p.kind === 'text' || p.kind === 'path' || p.kind === 'url'}>
                  <span class="label">{t(`workflow.param.${p.name}` as 'workflow.param.folder')}{p.required ? ' *' : ''}</span>
                  {#if p.kind === 'bool'}
                    <input type="checkbox" checked={a.params[p.name] === true} onchange={(e) => setParam(a, p, (e.currentTarget as HTMLInputElement).checked)} />
                  {:else if p.options?.length && p.kind !== 'list'}
                    <select class="input" value={paramText(a, p)} onchange={(e) => setParam(a, p, (e.currentTarget as HTMLSelectElement).value)}>
                      <option value="">–</option>
                      {#each p.options as o (o)}<option value={o}>{o}</option>{/each}
                    </select>
                  {:else}
                    <input
                      class="input"
                      class:mono={p.kind === 'path' || p.kind === 'url'}
                      type={p.kind === 'number' ? 'number' : 'text'}
                      list={datalistFor(p.kind)}
                      value={paramText(a, p)}
                      placeholder={p.kind === 'list' || p.kind === 'tag' ? t('workflow.visual.commaSeparated') : p.kind === 'duration' ? '3d' : p.kind === 'text' ? '{{betreff}}' : ''}
                      onchange={(e) => setParam(a, p, (e.currentTarget as HTMLInputElement).value)}
                    />
                  {/if}
                </label>
              {/each}
            </div>
          {/if}
          <details class="adv">
            <summary>{t('workflow.visual.advanced')}</summary>
            <label class="field">
              <span class="label">{t('workflow.visual.actionIf')}</span>
              <input class="input mono" bind:value={a.if} placeholder={EX.actionIf} />
            </label>
            <label class="chk"><input type="checkbox" bind:checked={a.continueOnError} /> {t('workflow.visual.continueOnError')}</label>
          </details>
        </li>
      {/each}
    </ol>
    <button class="btn btn-ghost add" onclick={addAction}><Plus size={14} /> {t('workflow.visual.addAction')}</button>
  </section>
</div>

{#snippet group(g: Cond & { kind: 'group' }, depth: number)}
  <div class="group" class:nested={depth > 0} role="group">
    <div class="g-head">
      <div class="seg sm" role="radiogroup" aria-label={t('workflow.visual.match')}>
        <button role="radio" aria-checked={g.match === 'all'} onclick={() => ((g.match = 'all'), syncCondition())}>{t('workflow.visual.all')}</button>
        <button role="radio" aria-checked={g.match === 'any'} onclick={() => ((g.match = 'any'), syncCondition())}>{t('workflow.visual.any')}</button>
      </div>
      <span class="g-hint">{g.match === 'all' ? t('workflow.visual.allHint') : t('workflow.visual.anyHint')}</span>
    </div>
    {#each g.items as item, i (i)}
      {#if i > 0}<span class="joiner">{g.match === 'all' ? t('workflow.visual.and') : t('workflow.visual.or')}</span>{/if}
      {#if item.kind === 'group'}
        <div class="nest-row">
          {@render group(item, depth + 1)}
          <button class="icon-btn" aria-label={t('common.action.delete')} onclick={() => removeAt(g, i)}><X size={14} /></button>
        </div>
      {:else}
        {@const kind = fieldKind(item.field)}
        <div class="row">
          <select class="input" bind:value={item.field} onchange={() => { if (!schema.operators[fieldKind(item.field)].includes(item.op)) item.op = schema.operators[fieldKind(item.field)][0]; syncCondition(); }} aria-label={t('workflow.visual.field')}>
            {#each schema.fields as f (f.name)}<option value={f.name}>{f.alias ? `${f.alias} (${f.name})` : f.name}</option>{/each}
          </select>
          <select class="input op" bind:value={item.op} onchange={syncCondition} aria-label={t('workflow.visual.operator')}>
            {#each schema.operators[kind] as o (o)}<option value={o}>{t(`workflow.op.${o}` as 'workflow.op.eq')}</option>{/each}
          </select>
          {#if !['is_true', 'is_false', 'empty', 'not_empty'].includes(item.op)}
            <input class="input" bind:value={item.value} oninput={syncCondition} aria-label={t('workflow.visual.value')} type={kind === 'number' ? 'number' : 'text'} list={item.field === 'mail.folder' ? uid + '-folders' : item.field === 'mail.tags' ? uid + '-tags' : undefined} />
          {:else}
            <span class="grow"></span>
          {/if}
          <button class="icon-btn" aria-label={t('common.action.delete')} onclick={() => removeAt(g, i)}><X size={14} /></button>
        </div>
      {/if}
    {/each}
    <div class="g-actions">
      <button class="btn btn-ghost sm" onclick={() => addRow(g)}><Plus size={13} /> {t('workflow.visual.addCondition')}</button>
      {#if depth === 0}<button class="btn btn-ghost sm" onclick={() => addGroup(g)}><Plus size={13} /> {t('workflow.visual.addGroup')}</button>{/if}
    </div>
  </div>
{/snippet}

<style>
  .vb {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .card {
    position: relative;
    padding: 12px 14px;
    border: 1px solid var(--tm-line);
    border-radius: 12px;
    background: var(--tm-surface);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .card + .card::before {
    content: '';
    position: absolute;
    left: 28px;
    top: -13px;
    height: 12px;
    border-left: 2px dashed var(--tm-line-strong);
  }
  .trig {
    border-left: 4px solid var(--tm-brand);
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-size: 13.5px;
    font-weight: 650;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
    border: none;
    padding: 0;
    margin: 0;
  }
  .checks {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
  }
  .chk {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
  }
  .chk input {
    accent-color: var(--tm-accent);
  }
  .hint {
    font-size: 12px;
    color: var(--tm-muted);
    margin: 0;
  }
  .warn {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-warn);
  }
  .mono {
    font-family: var(--tm-mono);
    font-size: 12.5px;
  }
  .seg.sm :global(button) {
    font-size: 12px;
    padding: 3px 9px;
  }
  .group {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 8px;
    border-radius: 10px;
    background: var(--tm-surface-2);
  }
  .group.nested {
    flex: 1;
    background: color-mix(in srgb, var(--tm-accent) 6%, var(--tm-surface));
    border: 1px dashed var(--tm-line-strong);
  }
  .g-head {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .g-hint {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .joiner {
    align-self: flex-start;
    margin-left: 10px;
    font-size: 10.5px;
    font-weight: 700;
    letter-spacing: 0.05em;
    text-transform: uppercase;
    color: var(--tm-brand-navy);
    background: color-mix(in srgb, var(--tm-brand-navy) 10%, transparent);
    padding: 0 6px;
    border-radius: 4px;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(150px, 1.2fr) minmax(110px, 0.8fr) 1.4fr auto;
    gap: 6px;
    align-items: center;
  }
  .nest-row {
    display: flex;
    gap: 6px;
    align-items: flex-start;
  }
  .g-actions {
    display: flex;
    gap: 6px;
  }
  .sm {
    font-size: 12px;
  }
  .grow {
    flex: 1;
  }
  .actions {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .action {
    border: 1px solid var(--tm-line);
    border-left: 4px solid var(--tm-success);
    border-radius: 10px;
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .a-head {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .a-head select {
    max-width: 280px;
  }
  .num {
    width: 22px;
    height: 22px;
    border-radius: 99px;
    background: var(--tm-success);
    color: #fff;
    font-size: 12px;
    font-weight: 700;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
  }
  .ext {
    font-size: 10.5px;
    font-weight: 700;
    padding: 1px 6px;
    border-radius: 99px;
    background: color-mix(in srgb, var(--tm-warn) 18%, transparent);
    color: var(--tm-warn);
  }
  .params {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
    gap: 8px 10px;
  }
  .params .wide {
    grid-column: 1 / -1;
  }
  .adv summary {
    font-size: 12px;
    color: var(--tm-muted);
    cursor: pointer;
  }
  .adv[open] {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .add {
    align-self: flex-start;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .row {
      grid-template-columns: 1fr 1fr;
    }
  }
</style>
