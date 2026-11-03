<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Create (from a template) or edit a project, incl. custom fields. -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import Modal from '../common/Modal.svelte';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { TAG_COLORS } from '../common/TagIcon.svelte';
  import type { CustomField, CustomFieldType, Project, ProjectInput, ProjectStatus, ProjectTemplate } from '../../types';

  interface Props {
    project?: Project;
    onclose: () => void;
    onsaved?: (p: Project) => void;
  }
  let { project, onclose, onsaved }: Props = $props();

  const STATUSES: ProjectStatus[] = ['planned', 'active', 'paused', 'done', 'archived'];
  const FIELD_TYPES: CustomFieldType[] = ['text', 'number', 'date', 'url', 'select'];

  let templates = $state<ProjectTemplate[]>([]);
  // svelte-ignore state_referenced_locally
  let templateId = $state(project?.templateId ?? 'blank');
  let error = $state('');
  let saving = $state(false);
  // svelte-ignore state_referenced_locally
  let p = $state<ProjectInput>(
    project
      ? (JSON.parse(JSON.stringify(project)) as ProjectInput)
      : { key: '', name: '', status: 'active', color: TAG_COLORS[0], description: '', customFields: [], memberPersonIds: [], notes: '', tagIds: [], rules: { enabled: false, query: '' } },
  );

  $effect(() => {
    void app.api.listProjectTemplates().then((x) => (templates = x));
  });

  function pickTemplate(tpl: ProjectTemplate) {
    templateId = tpl.id;
    p.color = tpl.color;
    p.customFields = tpl.fields.map((f) => ({ ...f }));
    p.templateId = tpl.id;
  }

  function addField() {
    p.customFields = [...p.customFields, { key: 'f' + (p.customFields.length + 1), label: '', type: 'text', value: '' }];
  }

  function removeField(i: number) {
    p.customFields = p.customFields.filter((_, k) => k !== i);
  }

  async function save(e: SubmitEvent) {
    e.preventDefault();
    if (!p.name.trim()) {
      error = t('projects.dialog.nameRequired');
      return;
    }
    saving = true;
    try {
      const input = $state.snapshot(p) as ProjectInput;
      const tpl = templates.find((x) => x.id === templateId);
      if (!project && tpl?.ruleQuery && !input.rules.query && input.key) input.rules = { enabled: true, query: tpl.ruleQuery.replace('{key}', input.key) };
      input.customFields = input.customFields.filter((f: CustomField) => f.label.trim());
      const saved = await app.api.saveProject(input);
      await app.loadProjects();
      onsaved?.(saved);
      onclose();
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      saving = false;
    }
  }
</script>

<Modal title={project ? t('projects.dialog.editTitle') : t('projects.dialog.newTitle')} {onclose} width="640px">
  <form id="project-form" onsubmit={save} class="form">
    {#if !project}
      <div class="tpls" role="radiogroup" aria-label={t('projects.dialog.template')}>
        {#each templates as tpl (tpl.id)}
          <button type="button" role="radio" aria-checked={templateId === tpl.id} class="tpl" style:--c={tpl.color} onclick={() => pickTemplate(tpl)}>
            <strong>{tpl.name}</strong>
            <small>{tpl.description}</small>
            {#if tpl.workflowTemplateIds.length}<span class="wf">{t('projects.dialog.workflows', { count: tpl.workflowTemplateIds.length })}</span>{/if}
          </button>
        {/each}
      </div>
    {/if}
    <div class="grid">
      <label class="field wide">
        <span class="label">{t('projects.field.name')}</span>
        <input class="input" bind:value={p.name} data-autofocus required />
      </label>
      <label class="field">
        <span class="label">{t('projects.field.key')}</span>
        <input class="input" bind:value={p.key} placeholder="HB-26" spellcheck="false" />
      </label>
      <label class="field">
        <span class="label">{t('projects.field.status')}</span>
        <select class="input" bind:value={p.status}>
          {#each STATUSES as s (s)}<option value={s}>{t(`projects.status.${s}`)}</option>{/each}
        </select>
      </label>
      <label class="field">
        <span class="label">{t('projects.field.customer')}</span>
        <input class="input" bind:value={p.customer} />
      </label>
      <div class="field">
        <span class="label">{t('projects.field.color')}</span>
        <div class="swatches">
          {#each TAG_COLORS as c (c)}<button type="button" class="sw" style:background={c} aria-pressed={p.color === c} aria-label={c} onclick={() => (p.color = c)}></button>{/each}
        </div>
      </div>
      <label class="field">
        <span class="label">{t('projects.field.start')}</span>
        <input class="input" type="date" bind:value={p.startDate} />
      </label>
      <label class="field">
        <span class="label">{t('projects.field.due')}</span>
        <input class="input" type="date" bind:value={p.dueDate} />
      </label>
      <label class="field wide">
        <span class="label">{t('projects.field.description')}</span>
        <textarea class="input" rows="2" bind:value={p.description}></textarea>
      </label>
    </div>
    <fieldset>
      <legend>{t('projects.field.custom')}</legend>
      {#each p.customFields as f, i (i)}
        <div class="cf">
          <input class="input" bind:value={f.label} placeholder={t('projects.dialog.fieldLabel')} aria-label={t('projects.dialog.fieldLabel')} />
          <select class="input" bind:value={f.type} aria-label={t('projects.dialog.fieldType')}>
            {#each FIELD_TYPES as ft (ft)}<option value={ft}>{t(`projects.fieldType.${ft}`)}</option>{/each}
          </select>
          {#if f.type === 'select'}
            <select class="input" bind:value={f.value} aria-label={f.label}>
              {#each f.options ?? [] as o (o)}<option>{o}</option>{/each}
            </select>
          {:else}
            <input class="input" type={f.type === 'number' ? 'number' : f.type === 'date' ? 'date' : f.type === 'url' ? 'url' : 'text'} bind:value={f.value} aria-label={f.label} />
          {/if}
          <button type="button" class="icon-btn" aria-label={t('common.action.delete')} onclick={() => removeField(i)}><Trash size={14} /></button>
        </div>
      {/each}
      <button type="button" class="btn btn-ghost sm" onclick={addField}><Plus size={14} /> {t('projects.dialog.addField')}</button>
    </fieldset>
    {#if error}<p class="err" role="alert">{error}</p>{/if}
  </form>
  {#snippet footer()}
    <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
    <button class="btn btn-primary" type="submit" form="project-form" disabled={saving}>{project ? t('common.action.save') : t('projects.dialog.create')}</button>
  {/snippet}
</Modal>

<style>
  .form {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .tpls {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 8px;
  }
  .tpl {
    display: flex;
    flex-direction: column;
    gap: 3px;
    padding: 10px;
    border: 1px solid var(--tm-line);
    border-left: 4px solid var(--c);
    border-radius: 10px;
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .tpl[aria-checked='true'] {
    border-color: var(--c);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--c) 30%, transparent);
  }
  .tpl strong {
    font-size: 13px;
  }
  .tpl small {
    font-size: 11.5px;
    color: var(--tm-muted);
    line-height: 1.3;
  }
  .wf {
    font-size: 10.5px;
    color: var(--tm-accent-text);
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px 14px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .wide {
    grid-column: span 2;
  }
  textarea {
    resize: vertical;
    font: inherit;
  }
  .swatches {
    display: flex;
    gap: 6px;
    flex-wrap: wrap;
    padding-top: 4px;
  }
  .sw {
    width: 22px;
    height: 22px;
    border-radius: 99px;
    border: 2px solid transparent;
    cursor: pointer;
  }
  .sw[aria-pressed='true'] {
    border-color: var(--tm-fg);
  }
  fieldset {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 8px 12px 10px;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  legend {
    font-size: 12.5px;
    font-weight: 600;
    padding: 0 4px;
  }
  .cf {
    display: grid;
    grid-template-columns: 1.2fr 0.8fr 1.4fr auto;
    gap: 6px;
    align-items: center;
  }
  .sm {
    align-self: flex-start;
    font-size: 12.5px;
  }
  .err {
    margin: 0;
    color: var(--tm-danger);
    font-size: 12.5px;
  }
  @media (max-width: 639px) {
    .grid,
    .cf {
      grid-template-columns: 1fr;
    }
    .wide {
      grid-column: auto;
    }
  }
</style>
