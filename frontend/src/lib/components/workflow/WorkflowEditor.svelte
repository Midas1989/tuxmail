<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Workflow editor with three modes on one model:
  visual builder | expert one-line rule syntax (chips + autocomplete) | YAML
  (highlighting, line numbers, inline diagnostics from Api.validateWorkflow,
  completion from Api.completeWorkflow). Switching is lossless where the
  target can express everything; otherwise the editor warns first.
-->
<script lang="ts">
  import Blocks from '@lucide/svelte/icons/blocks';
  import Braces from '@lucide/svelte/icons/braces';
  import SquareFunction from '@lucide/svelte/icons/square-function';
  import FlaskConical from '@lucide/svelte/icons/flask-conical';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import Modal from '../common/Modal.svelte';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import CodeEditor from './CodeEditor.svelte';
  import VisualBuilder from './VisualBuilder.svelte';
  import DryRunPanel from './DryRunPanel.svelte';
  import { app } from '../../state';
  import { i18n, t, type MessageKey } from '../../i18n/index.svelte';
  import type { Workflow, WorkflowDiagnostic, WorkflowDryRunResult, WorkflowEditorMode, WorkflowSchema } from '../../types';
  import { ApiError } from '../../api/api';
  import { workflowToRule, workflowToYamlChecked } from '../../workflow/lang';
  import { hasExternalEffects } from '../../workflow/schema';

  interface Props {
    workflow: Workflow;
    schema: WorkflowSchema;
    initialMode?: WorkflowEditorMode;
    onclose: () => void;
    onsaved: (w: Workflow) => void;
  }
  let { workflow, schema, initialMode = 'visual', onclose, onsaved }: Props = $props();

  const ctx = $derived(app.workflowContext);
  // svelte-ignore state_referenced_locally
  let model = $state<Workflow>(JSON.parse(JSON.stringify(workflow)) as Workflow);
  // svelte-ignore state_referenced_locally
  const wasEnabled = workflow.enabled;
  let mode = $state<WorkflowEditorMode>('visual');
  let src = $state('');
  let diags = $state<WorkflowDiagnostic[]>([]);
  let lossWarning = $state(false);
  let pendingMode = $state<WorkflowEditorMode | null>(null);
  let confirmExternal = $state(false);
  let dry = $state<WorkflowDryRunResult | null>(null);
  let dryLoading = $state(false);
  let saveError = $state('');
  let editor = $state<CodeEditor>();

  const MODES: { id: WorkflowEditorMode; icon: typeof Blocks }[] = [
    { id: 'visual', icon: Blocks },
    { id: 'rule', icon: SquareFunction },
    { id: 'yaml', icon: Braces },
  ];

  $effect(() => {
    // svelte-ignore state_referenced_locally
    if (initialMode !== 'visual') void enter(initialMode);
  });

  function sourceFor(m: 'rule' | 'yaml'): { src: string; lossless: boolean } {
    const snap = $state.snapshot(model) as Workflow;
    return m === 'rule' ? workflowToRule(snap, schema, ctx) : workflowToYamlChecked(snap, schema);
  }

  async function enter(m: WorkflowEditorMode) {
    if (m === 'visual') {
      mode = 'visual';
      diags = [];
      return;
    }
    const r = sourceFor(m);
    src = r.src;
    lossWarning = !r.lossless;
    mode = m;
    await validate();
  }

  /** Parse the current source into the model; false when it has errors. */
  async function commitSource(): Promise<boolean> {
    if (mode === 'visual') return true;
    const v = await app.api.validateWorkflow(src, mode);
    diags = v.diagnostics;
    if (!v.workflow) return false;
    const keep = { id: model.id, projectId: model.projectId, templateId: model.templateId, lastRun: model.lastRun };
    model = mode === 'rule' ? { ...model, trigger: v.workflow.trigger, condition: v.workflow.condition, actions: v.workflow.actions, externalEffects: v.workflow.externalEffects } : { ...v.workflow, ...keep, id: v.workflow.id || model.id };
    return true;
  }

  async function switchMode(m: WorkflowEditorMode) {
    if (m === mode) return;
    if (!(await commitSource())) {
      pendingMode = m;
      return;
    }
    await enter(m);
  }

  let vTimer: ReturnType<typeof setTimeout> | null = null;
  async function validate() {
    if (mode === 'visual') return;
    const m = mode;
    const s = src;
    const v = await app.api.validateWorkflow(s, m);
    if (mode === m && src === s) diags = v.diagnostics;
  }
  $effect(() => {
    void src;
    if (mode === 'visual') return;
    if (vTimer) clearTimeout(vTimer);
    vTimer = setTimeout(validate, 250);
  });

  function diagText(d: WorkflowDiagnostic): string {
    const k = 'workflow.diag.' + d.code;
    return i18n.has(k) ? t(k as MessageKey, { detail: d.detail ?? '' }) : d.message;
  }

  const errors = $derived(diags.filter((d) => d.severity === 'error').length);
  const external = $derived(hasExternalEffects(schema, model.actions.map((a) => a.type)));

  async function runDry() {
    if (!(await commitSource())) return;
    dryLoading = true;
    try {
      dry = await app.api.dryRunWorkflow($state.snapshot(model) as Workflow, { limit: 8 });
    } finally {
      dryLoading = false;
    }
  }

  async function save(confirmed = false) {
    saveError = '';
    if (!(await commitSource())) {
      saveError = t('workflow.editor.fixErrors');
      return;
    }
    if (!model.name.trim()) {
      saveError = t('workflow.editor.nameRequired');
      return;
    }
    if (model.enabled && !wasEnabled && external && !confirmed) {
      confirmExternal = true;
      return;
    }
    try {
      // The core only stores an enabled workflow with external effects when the request says the user confirmed it.
      const saved = await app.api.saveWorkflow({ ...($state.snapshot(model) as Workflow), confirmExternal: confirmed || undefined });
      onsaved(saved);
      onclose();
    } catch (err) {
      if (err instanceof ApiError && err.code === 'confirmation_required' && !confirmed) {
        confirmExternal = true; // e.g. a changed forward address of an already enabled workflow
        return;
      }
      saveError = err instanceof Error ? err.message : String(err);
    }
  }
</script>

<Modal title={workflow.id ? t('workflow.editor.editTitle') : t('workflow.editor.newTitle')} {onclose} width="min(1080px, 96vw)">
  <div class="ed">
    <div class="meta">
      <label class="field name">
        <span class="label">{t('workflow.editor.name')}</span>
        <input class="input" bind:value={model.name} data-autofocus />
      </label>
      <label class="field desc">
        <span class="label">{t('workflow.editor.description')}</span>
        <input class="input" bind:value={model.description} />
      </label>
      <label class="switch">
        <input type="checkbox" bind:checked={model.enabled} />
        <span class="track" aria-hidden="true"><span></span></span>
        {model.enabled ? t('workflow.enabled') : t('workflow.disabled')}
      </label>
    </div>

    <div class="modebar">
      <div class="seg" role="radiogroup" aria-label={t('workflow.editor.mode')}>
        {#each MODES as m (m.id)}
          {@const Icon = m.icon}
          <button role="radio" aria-checked={mode === m.id} onclick={() => switchMode(m.id)}><Icon size={14} /> {t(`workflow.mode.${m.id}`)}</button>
        {/each}
      </div>
      {#if external}<span class="ext"><TriangleAlert size={13} /> {t('workflow.externalHint')}</span>{/if}
      <span class="grow"></span>
      {#if mode !== 'visual'}
        <span class="status" class:bad={errors > 0} role="status">
          {#if errors}<CircleAlert size={14} /> {t('workflow.editor.errors', { count: errors })}{:else}<CircleCheck size={14} /> {t('workflow.editor.valid')}{/if}
        </span>
      {/if}
    </div>

    {#if lossWarning && mode !== 'visual'}
      <p class="loss" role="note"><TriangleAlert size={14} /> {t(mode === 'rule' ? 'workflow.editor.lossRule' : 'workflow.editor.lossYaml')}</p>
    {/if}

    <div class="body">
      {#if mode === 'visual'}
        <VisualBuilder bind:workflow={model} {schema} {ctx} />
      {:else}
        <CodeEditor
          bind:this={editor}
          bind:value={src}
          {mode}
          {schema}
          diagnostics={diags}
          complete={(s, off) => app.api.completeWorkflow(s, off, mode as 'rule' | 'yaml')}
          label={t(`workflow.mode.${mode}`)}
          placeholder={mode === 'rule' ? 'WHEN mail.received IF from:*@kunde.de THEN tag Kunde' : ''}
        />
        {#if mode === 'rule'}<p class="help">{t('workflow.editor.ruleHelp')}</p>{/if}
        {#if diags.length}
          <ul class="diags" aria-label={t('workflow.editor.problems')}>
            {#each diags as d, i (i)}
              <li class:err={d.severity === 'error'}>
                <button onclick={() => editor?.reveal(d)}>
                  {#if d.severity === 'error'}<CircleAlert size={13} />{:else}<TriangleAlert size={13} />{/if}
                  <span class="pos">{t('workflow.editor.position', { line: d.line, col: d.col })}</span>
                  <span>{diagText(d)}</span>
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      {/if}
    </div>

    {#if dry}
      <DryRunPanel result={dry} onclose={() => (dry = null)} />
    {/if}
    {#if saveError}<p class="err" role="alert">{saveError}</p>{/if}
  </div>
  {#snippet footer()}
    <button class="btn btn-ghost" onclick={runDry} disabled={dryLoading}><FlaskConical size={15} /> {t('workflow.dryRun')}</button>
    <span class="grow"></span>
    <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary" onclick={() => save()}>{t('common.action.save')}</button>
  {/snippet}
</Modal>

{#if pendingMode}
  {@const target = pendingMode}
  <ConfirmDialog
    title={t('workflow.editor.switchTitle')}
    message={t('workflow.editor.switchText')}
    confirmLabel={t('workflow.editor.switchDiscard')}
    danger
    onconfirm={() => {
      pendingMode = null;
      void enter(target);
    }}
    oncancel={() => (pendingMode = null)}
  />
{/if}
{#if confirmExternal}
  <ConfirmDialog
    title={t('workflow.confirmExternal.title')}
    message={t('workflow.confirmExternal.text')}
    confirmLabel={t('workflow.confirmExternal.ok')}
    onconfirm={() => {
      confirmExternal = false;
      void save(true);
    }}
    oncancel={() => (confirmExternal = false)}
  />
{/if}

<style>
  .ed {
    display: flex;
    flex-direction: column;
    gap: 12px;
    min-height: 0;
  }
  .meta {
    display: flex;
    gap: 10px;
    align-items: flex-end;
    flex-wrap: wrap;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .name {
    flex: 1 1 260px;
  }
  .desc {
    flex: 2 1 320px;
  }
  .switch {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    font-size: 13px;
    cursor: pointer;
  }
  .switch input {
    position: absolute;
    opacity: 0;
    width: 1px;
    height: 1px;
  }
  .track {
    width: 34px;
    height: 20px;
    border-radius: 99px;
    background: var(--tm-line-strong);
    position: relative;
    transition: background var(--tm-dur);
  }
  .track span {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    border-radius: 99px;
    background: #fff;
    transition: transform var(--tm-dur);
  }
  .switch input:checked + .track {
    background: var(--tm-success);
  }
  .switch input:checked + .track span {
    transform: translateX(14px);
  }
  .switch input:focus-visible + .track {
    outline: 2px solid var(--tm-focus);
    outline-offset: 2px;
  }
  .modebar {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  .ext {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 12px;
    color: var(--tm-warn);
  }
  .grow {
    flex: 1;
  }
  .status {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 12.5px;
    color: var(--tm-success);
  }
  .status.bad {
    color: var(--tm-danger);
  }
  .loss {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    padding: 6px 10px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--tm-warn) 12%, transparent);
    font-size: 12.5px;
  }
  .body {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .help {
    margin: 0;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .diags {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    overflow: hidden;
  }
  .diags li + li {
    border-top: 1px solid var(--tm-line);
  }
  .diags button {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 6px 10px;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 12.5px;
    text-align: left;
    cursor: pointer;
  }
  .diags button:hover {
    background: var(--tm-surface-2);
  }
  .diags li :global(svg) {
    color: var(--tm-warn);
    flex: none;
  }
  .diags li.err :global(svg) {
    color: var(--tm-danger);
  }
  .pos {
    font-family: var(--tm-mono);
    font-size: 11.5px;
    color: var(--tm-muted);
    flex: none;
  }
  .err {
    margin: 0;
    color: var(--tm-danger);
    font-size: 12.5px;
  }
</style>
