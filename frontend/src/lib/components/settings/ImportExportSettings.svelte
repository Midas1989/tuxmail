<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Import & Export (KONZEPT 13): import wizard (source type -> file -> options/mapping -> progress -> summary) and export. -->
<script lang="ts">
  import Upload from '@lucide/svelte/icons/upload';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import ArrowRight from '@lucide/svelte/icons/arrow-right';
  import Import from '@lucide/svelte/icons/import';
  import FileDown from '@lucide/svelte/icons/file-down';
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
  import { app } from '../../state';
  import { t, type MessageKey } from '../../i18n/index.svelte';
  import type { ExportFormat, ExportRequest, ImportFormat, ImportOptions, ImportPreview, ImportRequest, IOJob, IOSource } from '../../types';
  import IOJobProgress from './IOJobProgress.svelte';

  let { initialTab = 'import' }: { initialTab?: 'import' | 'export' } = $props();

  type Step = 'source' | 'file' | 'options' | 'progress';
  let tab = $state<'import' | 'export'>('import');
  $effect(() => {
    tab = initialTab;
  });
  let step = $state<Step>('source');
  let format = $state<ImportFormat>('mbox');
  let path = $state('');
  let uploadToken = $state('');
  let fileName = $state('');
  let dragging = $state(false);
  let busy = $state(false);
  let error = $state('');
  let preview = $state<ImportPreview | null>(null);
  let job = $state<IOJob | null>(null);
  let jobs = $state<IOJob[]>([]);
  let pstAvailable = $state(true);

  let opts = $state<ImportOptions>({ keepStructure: true, dedupe: true, keepFlags: true, keepDates: true, mboxVariant: 'mboxrd', duplicateMode: 'skip', tbAccounts: true, tbMail: true, tbAddressBooks: true });
  let targetKind = $state<'local' | 'folder'>('local');
  let targetFolder = $state(0);
  let mapping = $state<string[]>([]);

  const desktop = $derived(app.api.kind === 'wails');
  const mailFormats: ImportFormat[] = ['eml', 'mbox', 'maildir', 'pst'];
  const groups: { key: MessageKey; formats: ImportFormat[] }[] = [
    { key: 'importExport.group.mail', formats: ['eml', 'mbox', 'maildir', 'pst', 'thunderbird'] },
    { key: 'importExport.group.calendar', formats: ['ics'] },
    { key: 'importExport.group.contacts', formats: ['vcf', 'csv', 'ldif'] },
  ];
  const isMail = $derived(mailFormats.includes(format));
  const serverFolders = $derived(app.accounts.filter((a) => a.kind !== 'local').flatMap((a) => (app.folders[a.id] ?? []).map((f) => ({ id: f.id, label: `${a.displayName} / ${f.name}` }))));

  $effect(() => {
    app.api.listIOFormats().then((f) => (pstAvailable = f.pst)).catch(() => {});
    refreshJobs();
  });

  async function refreshJobs() {
    try {
      jobs = (await app.api.listIOJobs()).slice(0, 6);
    } catch {
      jobs = [];
    }
  }

  function fmtKey(f: string): MessageKey {
    return `importExport.format.${f}` as MessageKey;
  }
  function descKey(f: string): MessageKey {
    return `importExport.formatHint.${f}` as MessageKey;
  }

  function pick(f: ImportFormat) {
    format = f;
    path = uploadToken = fileName = error = '';
    preview = null;
    step = 'file';
  }

  async function upload(file: File) {
    busy = true;
    error = '';
    try {
      const up = await app.api.uploadImportFile(file, file.name);
      uploadToken = up.token;
      fileName = up.name;
      path = '';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    dragging = false;
    const f = e.dataTransfer?.files?.[0];
    if (f) upload(f);
  }

  function source(): IOSource {
    return { format, path: path || undefined, uploadToken: uploadToken || undefined };
  }

  async function inspect() {
    busy = true;
    error = '';
    try {
      preview = await app.api.inspectImport(source());
      mapping = preview.mapping ? [...preview.mapping] : [];
      step = 'options';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function start() {
    busy = true;
    error = '';
    const req: ImportRequest = {
      source: source(),
      target: { kind: isMail ? targetKind : 'local', folderId: targetKind === 'folder' ? targetFolder : undefined },
      options: { ...opts, csvMapping: format === 'csv' ? mapping : undefined },
    };
    try {
      job = await app.api.startImport(req);
      step = 'progress';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function resume(id: string) {
    error = '';
    try {
      job = await app.api.startImport({ source: { format: 'mbox' }, target: { kind: 'local' }, options: opts, resumeJobId: id });
      tab = 'import';
      step = 'progress';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  function restart() {
    step = 'source';
    job = null;
    preview = null;
    path = uploadToken = fileName = '';
    refreshJobs();
  }

  // ---- export ----
  let exFormat = $state<ExportFormat>('mbox');
  let exFolders = $state<number[]>([]);
  let exQuery = $state('');
  let exFrom = $state('');
  let exTo = $state('');
  let exVcard = $state<'3.0' | '4.0'>('4.0');
  let exCsv = $state<'outlook' | 'google'>('outlook');
  let exJob = $state<IOJob | null>(null);
  const exMail = $derived(['eml', 'mbox', 'maildir'].includes(exFormat));
  const exportFormats: ExportFormat[] = ['eml', 'mbox', 'maildir', 'ics', 'vcf', 'csv', 'ldif'];
  const allFolders = $derived(app.accounts.flatMap((a) => (app.folders[a.id] ?? []).map((f) => ({ id: f.id, label: `${a.displayName} / ${f.name}` }))));

  function toggleFolder(id: number) {
    exFolders = exFolders.includes(id) ? exFolders.filter((x) => x !== id) : [...exFolders, id];
  }

  async function startExport() {
    error = '';
    const req: ExportRequest = {
      format: exFormat,
      selection: exMail ? { folderIds: exFolders, query: exQuery || undefined } : undefined,
      from: exFormat === 'ics' ? exFrom || undefined : undefined,
      to: exFormat === 'ics' ? exTo || undefined : undefined,
      vcardVersion: exVcard,
      csvPreset: exCsv,
    };
    try {
      exJob = await app.api.startExport(req);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<section class="card wide" id="set-import-export" aria-labelledby="set-io-h">
  <div class="card-head">
    <h2 id="set-io-h">{t('importExport.title')}</h2>
    <div class="seg" role="tablist" aria-label={t('importExport.title')}>
      <button role="tab" aria-selected={tab === 'import'} onclick={() => (tab = 'import')}><Import size={14} /> {t('importExport.import')}</button>
      <button role="tab" aria-selected={tab === 'export'} onclick={() => (tab = 'export')}><FileDown size={14} /> {t('importExport.export')}</button>
    </div>
  </div>
  <p class="about">{t('importExport.intro')}</p>

  {#if tab === 'import'}
    <ol class="steps" aria-label={t('importExport.steps')}>
      {#each ['source', 'file', 'options', 'progress'] as s, i (s)}
        <li class:active={step === s}>{i + 1}. {t(`importExport.step.${s}` as MessageKey)}</li>
      {/each}
    </ol>

    {#if step === 'source'}
      {#each groups as g (g.key)}
        <h3>{t(g.key)}</h3>
        <div class="formats">
          {#each g.formats as f (f)}
            <button class="fmt" disabled={f === 'pst' && !pstAvailable} onclick={() => pick(f)}>
              <span class="fname">{t(fmtKey(f))}</span>
              <span class="fdesc">{t(descKey(f))}</span>
            </button>
          {/each}
        </div>
      {/each}
    {:else if step === 'file'}
      <div
        class="drop"
        class:dragging
        role="region"
        aria-label={t('importExport.dropLabel')}
        ondragover={(e) => {
          e.preventDefault();
          dragging = true;
        }}
        ondragleave={() => (dragging = false)}
        ondrop={onDrop}
      >
        <Upload size={22} />
        <p>{t(format === 'maildir' || format === 'thunderbird' ? 'importExport.dropZip' : 'importExport.drop', { format: t(fmtKey(format)) })}</p>
        <label class="btn">
          {t('importExport.chooseFile')}
          <input type="file" hidden onchange={(e) => { const f = (e.currentTarget as HTMLInputElement).files?.[0]; if (f) upload(f); }} />
        </label>
        {#if fileName}<p class="picked">{t('importExport.uploaded', { name: fileName })}</p>{/if}
      </div>
      {#if desktop}
        <label class="label" for="io-path">{t('importExport.localPath')}</label>
        <input id="io-path" class="input" bind:value={path} placeholder={t(format === 'thunderbird' ? 'importExport.pathHintThunderbird' : 'importExport.pathHint')} />
      {/if}
      <div class="nav">
        <button class="btn btn-ghost" onclick={() => (step = 'source')}><ArrowLeft size={14} /> {t('importExport.back')}</button>
        <button class="btn btn-primary" disabled={busy || (!path && !uploadToken)} onclick={inspect}>{t('importExport.next')} <ArrowRight size={14} /></button>
      </div>
    {:else if step === 'options' && preview}
      {#if preview.items}<p class="about">{t('importExport.found', { count: preview.items })}</p>{/if}
      {#if isMail}
        <fieldset>
          <legend>{t('importExport.target')}</legend>
          <label class="radio"><input type="radio" bind:group={targetKind} value="local" /> {t('importExport.targetLocal')}</label>
          <label class="radio"><input type="radio" bind:group={targetKind} value="folder" disabled={!serverFolders.length} /> {t('importExport.targetFolder')}</label>
          {#if targetKind === 'folder'}
            <select class="input" bind:value={targetFolder} aria-label={t('importExport.targetFolder')}>
              {#each serverFolders as f (f.id)}<option value={f.id}>{f.label}</option>{/each}
            </select>
            <p class="hint">{t('importExport.throttleHint')}</p>
          {/if}
        </fieldset>
        <fieldset>
          <legend>{t('importExport.options')}</legend>
          <label class="check"><input type="checkbox" bind:checked={opts.keepStructure} /> {t('importExport.keepStructure')}</label>
          <label class="check"><input type="checkbox" bind:checked={opts.dedupe} /> {t('importExport.dedupe')}</label>
          <label class="check"><input type="checkbox" bind:checked={opts.keepFlags} /> {t('importExport.keepFlags')}</label>
          <label class="check"><input type="checkbox" bind:checked={opts.keepDates} /> {t('importExport.keepDates')}</label>
          {#if format === 'mbox'}
            <label class="label" for="io-variant">{t('importExport.mboxVariant')}</label>
            <select id="io-variant" class="input" bind:value={opts.mboxVariant}>
              <option value="mboxrd">{t('importExport.mboxrd')}</option>
              <option value="mboxo">{t('importExport.mboxo')}</option>
            </select>
          {/if}
        </fieldset>
      {:else if format === 'ics'}
        <fieldset>
          <legend>{t('importExport.duplicates')}</legend>
          {#each ['skip', 'overwrite', 'copy'] as m (m)}
            <label class="radio"><input type="radio" bind:group={opts.duplicateMode} value={m} /> {t(`importExport.dup.${m}` as MessageKey)}</label>
          {/each}
        </fieldset>
      {:else if format === 'csv' && preview.header}
        <p class="about">{preview.preset ? t('importExport.presetDetected', { preset: t(`importExport.preset.${preview.preset}` as MessageKey) }) : t('importExport.presetNone')}</p>
        <table class="map">
          <thead><tr><th>{t('importExport.csvColumn')}</th><th>{t('importExport.csvSample')}</th><th>{t('importExport.csvField')}</th></tr></thead>
          <tbody>
            {#each preview.header as h, i (i)}
              <tr>
                <td>{h}</td>
                <td class="sample">{preview.sample?.[0]?.[i] ?? ''}</td>
                <td>
                  <select class="input" bind:value={mapping[i]} aria-label={t('importExport.csvFieldFor', { column: h })}>
                    <option value="">{t('importExport.csvIgnore')}</option>
                    {#each preview.fields ?? [] as f (f)}<option value={f}>{t(`importExport.field.${f}` as MessageKey)}</option>{/each}
                  </select>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else if format === 'thunderbird' && preview.profile}
        <div class="tb">
          <h3>{t('importExport.tbAccounts')}</h3>
          <ul>
            {#each preview.profile.accounts as acc (acc.key)}
              <li>{acc.name} <span class="muted">{acc.incoming.kind === 'none' ? t('importExport.tbLocal') : `${acc.incoming.kind} · ${acc.incoming.host ?? ''}`}</span></li>
            {/each}
          </ul>
          <h3>{t('importExport.tbAddressBooks')}</h3>
          <ul>
            {#each preview.profile.addressBooks ?? [] as ab (ab.file)}
              <li>{ab.name} <span class="muted">{t('importExport.tbContacts', { count: ab.count })}</span></li>
            {/each}
          </ul>
          {#if preview.profile.tbsync?.length}
            <p class="warn">{t('importExport.tbsyncHint')}</p>
            <ul>{#each preview.profile.tbsync as h (h)}<li class="muted">{h}</li>{/each}</ul>
          {/if}
          {#each preview.profile.warnings ?? [] as w (w)}<p class="warn">{w}</p>{/each}
          <label class="check"><input type="checkbox" bind:checked={opts.tbAccounts} /> {t('importExport.tbImportAccounts')}</label>
          <label class="check"><input type="checkbox" bind:checked={opts.tbMail} /> {t('importExport.tbImportMail')}</label>
          <label class="check"><input type="checkbox" bind:checked={opts.tbAddressBooks} /> {t('importExport.tbImportAddressBooks')}</label>
        </div>
      {/if}
      {#each preview.problems ?? [] as p (p)}<p class="warn">{p}</p>{/each}
      <div class="nav">
        <button class="btn btn-ghost" onclick={() => (step = 'file')}><ArrowLeft size={14} /> {t('importExport.back')}</button>
        <button class="btn btn-primary" disabled={busy} onclick={start}>{t('importExport.start')}</button>
      </div>
    {:else if step === 'progress' && job}
      <IOJobProgress {job} onfinished={() => refreshJobs()} />
      <div class="nav"><button class="btn" onclick={restart}>{t('importExport.newImport')}</button></div>
    {/if}
  {:else}
    <div class="export">
      <label class="label" for="ex-format">{t('importExport.exportFormat')}</label>
      <select id="ex-format" class="input" bind:value={exFormat}>
        {#each exportFormats as f (f)}<option value={f}>{t(fmtKey(f))}</option>{/each}
      </select>
      {#if exMail}
        <span class="label">{t('importExport.exportFolders')}</span>
        <div class="folders">
          {#each allFolders as f (f.id)}
            <label class="check"><input type="checkbox" checked={exFolders.includes(f.id)} onchange={() => toggleFolder(f.id)} /> {f.label}</label>
          {/each}
        </div>
        <label class="label" for="ex-query">{t('importExport.exportQuery')}</label>
        <input id="ex-query" class="input" bind:value={exQuery} placeholder={t('importExport.exportQueryHint')} />
      {:else if exFormat === 'ics'}
        <div class="range">
          <label class="label" for="ex-from">{t('importExport.from')}</label>
          <input id="ex-from" class="input" type="date" bind:value={exFrom} />
          <label class="label" for="ex-to">{t('importExport.to')}</label>
          <input id="ex-to" class="input" type="date" bind:value={exTo} />
        </div>
      {:else if exFormat === 'vcf'}
        <label class="label" for="ex-vc">{t('importExport.vcardVersion')}</label>
        <select id="ex-vc" class="input" bind:value={exVcard}><option value="4.0">4.0</option><option value="3.0">3.0</option></select>
      {:else if exFormat === 'csv'}
        <label class="label" for="ex-csv">{t('importExport.csvLayout')}</label>
        <select id="ex-csv" class="input" bind:value={exCsv}>
          <option value="outlook">{t('importExport.preset.outlook')}</option>
          <option value="google">{t('importExport.preset.google')}</option>
        </select>
      {/if}
      <p class="hint">{t('importExport.pdfHint')}</p>
      <div class="nav"><button class="btn btn-primary" disabled={exMail && !exFolders.length && !exQuery} onclick={startExport}>{t('importExport.startExport')}</button></div>
      {#if exJob}
        {#key exJob.id}<IOJobProgress job={exJob} />{/key}
      {/if}
    </div>
  {/if}

  {#if error}<p class="err" role="alert">{error}</p>{/if}

  {#if jobs.length}
    <h3>{t('importExport.recent')}</h3>
    <ul class="jobs">
      {#each jobs as j (j.id)}
        <li>
          <span>{t(`importExport.jobKind.${j.kind}`)} · {t(fmtKey(j.format === 'txmb' ? 'txmb' : j.format))}</span>
          <span class="muted">{t(`importExport.status.${j.status}`)} · {j.done}</span>
          {#if j.resumable}<button class="btn btn-ghost" onclick={() => resume(j.id)}><RotateCcw size={13} /> {t('importExport.resume')}</button>{/if}
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .wide {
    grid-column: 1 / -1;
  }
  .card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  h3 {
    margin: 6px 0 0;
    font-size: 12.5px;
    font-weight: 650;
    color: var(--tm-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .about,
  .hint {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .seg {
    display: inline-flex;
    border: 1px solid var(--tm-line-strong);
    border-radius: var(--tm-radius-sm);
    overflow: hidden;
  }
  .seg button {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    border: 0;
    background: transparent;
    padding: 5px 12px;
    font: inherit;
    font-size: 13px;
    color: var(--tm-fg);
    cursor: pointer;
  }
  .seg button[aria-selected='true'] {
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
  }
  .steps {
    display: flex;
    gap: 4px;
    margin: 0;
    padding: 0;
    list-style: none;
    font-size: 12px;
  }
  .steps li {
    flex: 1;
    padding: 6px 8px;
    border-bottom: 3px solid var(--tm-line);
    color: var(--tm-muted);
  }
  .steps li.active {
    border-color: var(--tm-highlight);
    color: var(--tm-fg);
    font-weight: 600;
  }
  .formats {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(190px, 1fr));
    gap: 8px;
  }
  .fmt {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 3px;
    padding: 10px 12px;
    border: 1px solid var(--tm-line-strong);
    border-radius: var(--tm-radius);
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .fmt:hover:not(:disabled) {
    border-color: var(--tm-accent);
    background: var(--tm-accent-soft);
  }
  .fmt:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .fname {
    font-weight: 650;
    font-size: 13.5px;
  }
  .fdesc {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .drop {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 26px;
    border: 2px dashed var(--tm-line-strong);
    border-radius: var(--tm-radius);
    color: var(--tm-muted);
    text-align: center;
  }
  .drop.dragging {
    border-color: var(--tm-accent);
    background: var(--tm-accent-soft);
  }
  .drop p {
    margin: 0;
    font-size: 13px;
  }
  .picked {
    color: var(--tm-success);
    font-weight: 600;
  }
  .nav {
    display: flex;
    justify-content: space-between;
    gap: 8px;
  }
  fieldset {
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    padding: 10px 12px;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  legend {
    font-size: 12px;
    font-weight: 650;
    color: var(--tm-muted);
    padding: 0 4px;
  }
  .check,
  .radio {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .map {
    width: 100%;
    border-collapse: collapse;
    font-size: 12.5px;
  }
  .map th {
    text-align: left;
    color: var(--tm-muted);
    font-weight: 600;
    padding: 4px 6px;
  }
  .map td {
    padding: 3px 6px;
    border-top: 1px solid var(--tm-line);
  }
  .sample,
  .muted {
    color: var(--tm-muted);
  }
  .tb ul,
  .jobs {
    margin: 4px 0;
    padding-left: 18px;
    font-size: 13px;
  }
  .warn {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-warn);
  }
  .err {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .export {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .folders {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
    gap: 4px 12px;
    max-height: 180px;
    overflow: auto;
  }
  .range {
    display: grid;
    grid-template-columns: auto 1fr auto 1fr;
    align-items: center;
    gap: 6px 10px;
  }
  .jobs {
    list-style: none;
    padding: 0;
  }
  .jobs li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 4px 0;
    border-bottom: 1px solid var(--tm-line);
  }
</style>
