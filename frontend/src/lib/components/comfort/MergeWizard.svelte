<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Mail merge wizard (KONZEPT 8.9): recipients from a contact group or a
  CSV/XLSX file, placeholders, preview per recipient, throttled sending
  as individual mails with a log and retry.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import Upload from '@lucide/svelte/icons/upload';
  import Users from '@lucide/svelte/icons/users';
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import { app, comfort } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import Modal from '../common/Modal.svelte';
  import type { MergeJob, MergeRecipient, MergeRendered } from '../../api/comfort';

  interface Props {
    identityId: number;
    subject: string;
    html: string;
    text: string;
    onclose: () => void;
  }
  let { identityId, subject, html, text, onclose }: Props = $props();

  type Step = 'recipients' | 'template' | 'preview' | 'send';
  const STEPS: Step[] = ['recipients', 'template', 'preview', 'send'];
  let step = $state<Step>('recipients');
  let recipients = $state<MergeRecipient[]>([]);
  let columns = $state<string[]>([]);
  let groups = $state<{ name: string; count: number }[]>([]);
  let group = $state('');
  let tplSubject = $state(untrack(() => subject));
  let tplText = $state(untrack(() => text.replace(/\n-- \n[\s\S]*$/, '')));
  let useHtml = $state(untrack(() => !!html));
  let perMinute = $state(30);
  let unsubscribe = $state('');
  let encrypt = $state(true);
  let previews = $state<MergeRendered[]>([]);
  let pi = $state(0);
  let job = $state<MergeJob | null>(null);
  let error = $state<string | null>(null);
  let fileInput = $state<HTMLInputElement>();
  let poll: ReturnType<typeof setInterval> | undefined;

  $effect(() => {
    void app.api.listContactGroups?.().then((g) => (groups = g.map((x) => ({ name: x.name, count: x.count })))).catch(() => {});
    void comfort.loadSettings().then((s) => s && (perMinute = s.merge.perMinute));
    return () => clearInterval(poll);
  });

  async function fromGroup() {
    if (!group) return;
    const r = await comfort.api.mergeFromGroup(group);
    recipients = r.recipients;
    columns = r.columns;
  }

  async function fromFile(f: File | undefined) {
    if (!f) return;
    error = null;
    try {
      const r = await comfort.api.parseMergeTable(f);
      recipients = r.recipients;
      columns = r.columns;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  function insert(col: string) {
    tplText += `{{${col}}}`;
  }

  function input() {
    return {
      identityId,
      template: { subject: tplSubject, text: tplText, html: useHtml ? html : undefined },
      recipients: $state.snapshot(recipients) as MergeRecipient[],
      perMinute,
      unsubscribe: unsubscribe || undefined,
      encrypt,
    };
  }

  async function toPreview() {
    previews = await comfort.api.previewMerge(input());
    pi = 0;
    step = 'preview';
  }

  async function start() {
    error = null;
    try {
      job = await comfort.api.startMerge(input());
      step = 'send';
      poll = setInterval(async () => {
        if (!job) return;
        job = await comfort.api.getMergeJob(job.id);
        if (job.state !== 'running') clearInterval(poll);
      }, 1500);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function retry() {
    if (!job) return;
    job = await comfort.api.retryMergeJob(job.id);
  }

  async function cancel() {
    if (job) await comfort.api.cancelMergeJob(job.id);
    clearInterval(poll);
    onclose();
  }

  const pv = $derived(previews[pi]);
  const minutes = $derived(Math.ceil(recipients.length / Math.max(1, perMinute)));
</script>

<Modal title={t('comfort.merge.title')} onclose={job?.state === 'running' ? () => {} : onclose} width="760px">
  <ol class="steps">
    {#each STEPS as s (s)}
      <li class:active={step === s}>{t(`comfort.merge.step.${s}`)}</li>
    {/each}
  </ol>

  {#if step === 'recipients'}
    <div class="row">
      <div class="col">
        <span class="label"><Users size={14} /> {t('comfort.merge.fromGroup')}</span>
        <div class="inline">
          <select class="input" bind:value={group}>
            <option value="">{t('comfort.merge.chooseGroup')}</option>
            {#each groups as g (g.name)}<option value={g.name}>{g.name} ({g.count})</option>{/each}
          </select>
          <button class="btn" disabled={!group} onclick={fromGroup}>{t('comfort.merge.load')}</button>
        </div>
      </div>
      <div class="col">
        <span class="label"><Upload size={14} /> {t('comfort.merge.fromFile')}</span>
        <button class="btn" onclick={() => fileInput?.click()}>{t('comfort.merge.chooseFile')}</button>
        <input type="file" accept=".csv,.tsv,.txt,.xlsx" hidden bind:this={fileInput} onchange={(e) => fromFile((e.currentTarget as HTMLInputElement).files?.[0])} />
      </div>
    </div>
    {#if recipients.length}
      <p class="info">{t('comfort.merge.recipientsLoaded', { count: recipients.length })}</p>
      <div class="table-wrap">
        <table>
          <thead><tr>{#each columns.slice(0, 5) as c (c)}<th>{c}</th>{/each}</tr></thead>
          <tbody>
            {#each recipients.slice(0, 6) as r (r.email)}
              <tr>{#each columns.slice(0, 5) as c (c)}<td>{r.fields?.[c] ?? (c.includes('mail') ? r.email : '')}</td>{/each}</tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  {:else if step === 'template'}
    <label class="field"><span class="label">{t('compose.subject')}</span><input class="input" bind:value={tplSubject} /></label>
    <div class="ph">
      <span class="label">{t('comfort.merge.placeholders')}</span>
      {#each ['vorname', 'nachname', 'name', 'firma', ...columns.filter((c) => !['vorname', 'nachname', 'name', 'firma'].includes(c))] as c (c)}
        <button class="chip" onclick={() => insert(c)}>{`{{${c}}}`}</button>
      {/each}
    </div>
    <label class="field"><span class="label">{t('comfort.merge.text')}</span><textarea class="input" rows="9" bind:value={tplText}></textarea></label>
    {#if html}<label class="chk"><input type="checkbox" bind:checked={useHtml} /> {t('comfort.merge.useHtml')}</label>{/if}
    <div class="row">
      <label class="field"><span class="label">{t('comfort.merge.perMinute')}</span><input class="input" type="number" min="1" max="600" bind:value={perMinute} /></label>
      <label class="field grow"><span class="label">{t('comfort.merge.unsubscribe')}</span><input class="input" bind:value={unsubscribe} placeholder={t('comfort.merge.unsubscribePlaceholder')} /></label>
    </div>
    <label class="chk"><input type="checkbox" bind:checked={encrypt} /> {t('comfort.merge.encrypt')}</label>
  {:else if step === 'preview'}
    {#if pv}
      <div class="pv-nav">
        <button class="icon-btn" aria-label={t('comfort.merge.prev')} disabled={pi === 0} onclick={() => pi--}><ChevronLeft size={16} /></button>
        <span>{t('comfort.merge.previewOf', { n: pi + 1, total: previews.length, email: pv.recipient.email })}</span>
        <button class="icon-btn" aria-label={t('comfort.merge.next')} disabled={pi >= previews.length - 1} onclick={() => pi++}><ChevronRight size={16} /></button>
      </div>
      {#if pv.missing?.length}<p class="warn">{t('comfort.merge.missing', { fields: pv.missing.join(', ') })}</p>{/if}
      <div class="pv">
        <div class="pv-subj">{pv.subject}</div>
        <pre>{pv.text}</pre>
      </div>
      <p class="info">{t('comfort.merge.duration', { count: recipients.length, minutes })}</p>
    {/if}
  {:else if job}
    <div class="progress">
      <div class="bar"><span style:width="{(100 * (job.sent + job.failed)) / Math.max(1, job.log.length)}%"></span></div>
      <p>{t('comfort.merge.status', { sent: job.sent, failed: job.failed, pending: job.pending })} · {t(`comfort.merge.state.${job.state}`)}</p>
    </div>
    <div class="table-wrap log">
      <table>
        <tbody>
          {#each job.log as e (e.index)}
            <tr class={e.status}><td>{e.email}</td><td>{t(`comfort.merge.entry.${e.status}`)}</td><td class="err">{e.error ?? ''}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}

  {#snippet footer()}
    {#if step === 'recipients'}
      <button class="btn" onclick={onclose}>{t('common.action.cancel')}</button>
      <button class="btn btn-primary" disabled={!recipients.length} onclick={() => (step = 'template')}>{t('comfort.merge.next')}</button>
    {:else if step === 'template'}
      <button class="btn" onclick={() => (step = 'recipients')}>{t('common.action.back')}</button>
      <button class="btn btn-primary" disabled={!tplSubject.trim()} onclick={toPreview}>{t('comfort.merge.toPreview')}</button>
    {:else if step === 'preview'}
      <button class="btn" onclick={() => (step = 'template')}>{t('common.action.back')}</button>
      <button class="btn btn-primary" onclick={start}>{t('comfort.merge.send', { count: recipients.length })}</button>
    {:else}
      {#if job?.failed}<button class="btn" onclick={retry}>{t('comfort.merge.retry')}</button>{/if}
      {#if job?.state === 'running'}<button class="btn" onclick={cancel}>{t('comfort.merge.stop')}</button>{/if}
      <button class="btn btn-primary" disabled={job?.state === 'running'} onclick={onclose}>{t('common.action.close')}</button>
    {/if}
  {/snippet}
</Modal>

<style>
  .steps {
    display: flex;
    gap: 6px;
    list-style: none;
    padding: 0;
    margin: 0 0 14px;
    font-size: 12.5px;
    counter-reset: s;
  }
  .steps li {
    flex: 1;
    padding: 6px 8px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--tm-line) 50%, transparent);
    color: var(--tm-muted);
    counter-increment: s;
  }
  .steps li::before {
    content: counter(s) '. ';
  }
  .steps li.active {
    background: color-mix(in srgb, var(--tm-accent) 16%, transparent);
    color: var(--tm-accent-text);
    font-weight: 600;
  }
  .row {
    display: flex;
    gap: 14px;
    flex-wrap: wrap;
  }
  .col {
    flex: 1;
    min-width: 220px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .inline {
    display: flex;
    gap: 6px;
  }
  .label {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 4px;
    margin-bottom: 10px;
  }
  .grow {
    flex: 1;
  }
  .info {
    font-size: 13px;
    color: var(--tm-muted);
  }
  .warn {
    color: var(--tm-warn);
    font-size: 13px;
  }
  .error {
    color: #dc2626;
  }
  .table-wrap {
    max-height: 220px;
    overflow: auto;
    border: 1px solid var(--tm-line);
    border-radius: 8px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 12.5px;
  }
  th,
  td {
    text-align: left;
    padding: 4px 8px;
    border-bottom: 1px solid var(--tm-line);
  }
  .ph {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    margin-bottom: 10px;
  }
  .chip {
    border: 1px solid var(--tm-line);
    background: var(--tm-surface);
    border-radius: 999px;
    padding: 2px 9px;
    font: inherit;
    font-size: 12px;
    font-family: var(--tm-mono, monospace);
    cursor: pointer;
    color: var(--tm-fg);
  }
  .chk {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 13px;
    margin-bottom: 8px;
  }
  .pv-nav {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 13px;
  }
  .pv {
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    padding: 12px 14px;
    margin-top: 8px;
  }
  .pv-subj {
    font-weight: 650;
    margin-bottom: 8px;
  }
  .pv pre {
    white-space: pre-wrap;
    font: inherit;
    margin: 0;
  }
  .progress .bar {
    height: 8px;
    border-radius: 4px;
    background: var(--tm-line);
    overflow: hidden;
  }
  .progress .bar span {
    display: block;
    height: 100%;
    background: var(--tm-accent);
    transition: width 0.3s;
  }
  tr.failed td {
    color: #dc2626;
  }
  tr.sent td:nth-child(2) {
    color: #16a34a;
  }
  .err {
    font-size: 11.5px;
  }
</style>
