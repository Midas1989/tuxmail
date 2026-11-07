<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Backup & Restore (KONZEPT 14): backup now (scope tree, passphrase, target), schedules, restore wizard. -->
<script lang="ts">
  import { isNativeShell } from '../../platform';
  import { buildReauth } from '../../security';
  import DatabaseBackup from '@lucide/svelte/icons/database-backup';
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
  import Play from '@lucide/svelte/icons/play';
  import Trash from '@lucide/svelte/icons/trash';
  import Plus from '@lucide/svelte/icons/plus';
  import Upload from '@lucide/svelte/icons/upload';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import CalendarClock from '@lucide/svelte/icons/calendar-clock';
  import { app } from '../../state';
  import { t, fmt, type MessageKey } from '../../i18n/index.svelte';
  import type { BackupArea, BackupInspection, BackupMode, BackupSchedule, BackupScheduleInput, BackupSource, IOJob, RestoreResult } from '../../types';
  import IOJobProgress from './IOJobProgress.svelte';

  let { initialTab = 'backup' }: { initialTab?: 'backup' | 'restore' } = $props();
  let tab = $state<'backup' | 'restore'>('backup');
  $effect(() => {
    tab = initialTab;
  });
  const desktop = $derived(app.api.kind === 'wails');
  const modes: BackupMode[] = ['config', 'local', 'full', 'custom'];
  const areas: BackupArea[] = ['mail', 'calendar', 'contacts', 'chats', 'settings'];
  let error = $state('');

  // ---- backup now ----
  let mode = $state<BackupMode>('full');
  let selAccounts = $state<number[]>([]);
  let selAreas = $state<BackupArea[]>([...areas]);
  let secrets = $state(false);
  // On a server, secrets in or out of an archive need a fresh proof; the app has none to give.
  const serverAccount = app.api.kind === 'http';
  const native = isNativeShell();
  let reauthPassword = $state('');
  let reauthCode = $state('');
  let pass = $state('');
  let pass2 = $state('');
  let targetKind = $state<'download' | 'local' | 'webdav'>('download');
  let targetPath = $state('');
  let davUrl = $state('');
  let davUser = $state('');
  let davPass = $state('');
  let backupJob = $state<IOJob | null>(null);
  const messengerSelected = $derived(app.accounts.some((a) => ['whatsapp', 'signal', 'matrix_bridge'].includes(a.kind) && (mode === 'full' || (mode === 'custom' && selAccounts.includes(a.id)))));
  const needPass = $derived(secrets || messengerSelected);
  const passOk = $derived((!needPass && !pass) || (pass.length >= 8 && pass === pass2));

  $effect(() => {
    if (!selAccounts.length && app.accounts.length) selAccounts = app.accounts.map((a) => a.id);
  });

  function toggle<T>(list: T[], v: T): T[] {
    return list.includes(v) ? list.filter((x) => x !== v) : [...list, v];
  }

  async function backupNow() {
    error = '';
    try {
      backupJob = await app.api.createBackup({
        scope: { mode, accounts: mode === 'custom' ? selAccounts : undefined, areas: mode === 'custom' ? selAreas : undefined, secrets },
        reauth: secrets && serverAccount ? buildReauth({ hasPassword: true, password: reauthPassword, secondFactor: reauthCode }) : undefined,
        passphrase: pass || undefined,
        target: { kind: targetKind, path: targetPath || undefined, url: davUrl || undefined, username: davUser || undefined, password: davPass || undefined },
      });
      pass = pass2 = reauthPassword = reauthCode = '';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  // ---- schedules ----
  let schedules = $state<BackupSchedule[]>([]);
  let editing = $state<BackupScheduleInput | null>(null);
  let schedJob = $state<IOJob | null>(null);

  async function loadSchedules() {
    try {
      schedules = await app.api.listBackupSchedules();
    } catch {
      schedules = [];
    }
  }
  $effect(() => {
    loadSchedules();
  });

  function newSchedule() {
    editing = { id: '', name: t('backup.schedule.defaultName'), enabled: true, frequency: 'daily', hour: 2, minute: 30, weekday: 0, scope: { mode: 'full' },
      target: { kind: desktop ? 'local' : 'webdav' }, incremental: true, keepDaily: 7, keepWeekly: 4, passphrase: '' };
  }

  async function saveSchedule() {
    if (!editing) return;
    error = '';
    try {
      await app.api.saveBackupSchedule(editing);
      editing = null;
      await loadSchedules();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function runNow(id: string) {
    error = '';
    try {
      schedJob = await app.api.runBackupSchedule(id);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function removeSchedule(id: string) {
    await app.api.deleteBackupSchedule(id);
    await loadSchedules();
  }

  // ---- restore ----
  type RStep = 'source' | 'inspect' | 'progress';
  let rstep = $state<RStep>('source');
  let rsource = $state<BackupSource>({});
  let rname = $state('');
  let rpath = $state('');
  let rpass = $state('');
  let inspection = $state<BackupInspection | null>(null);
  let baseToken = $state('');
  let rmode = $state<'replace' | 'merge'>('merge');
  let rAccounts = $state<number[]>([]);
  let rConfigOnly = $state(false);
  let rSecrets = $state(true);
  let restoreJob = $state<IOJob | null>(null);
  let restoreResult = $state<RestoreResult | null>(null);
  let busy = $state(false);

  async function uploadArchive(file: File, base = false) {
    busy = true;
    error = '';
    try {
      const up = await app.api.uploadImportFile(file, file.name);
      if (base) baseToken = up.token;
      else {
        rsource = { uploadToken: up.token };
        rname = up.name;
      }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function inspect() {
    busy = true;
    error = '';
    try {
      const src = rpath ? { path: rpath } : rsource;
      rsource = src;
      inspection = await app.api.inspectBackup(src, rpass);
      if (inspection.manifest) {
        rAccounts = (inspection.manifest.accounts ?? []).map((a) => a.id);
        rSecrets = inspection.manifest.secrets && !native;
        rstep = 'inspect';
      }
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  async function restore() {
    if (!inspection?.manifest) return;
    error = '';
    try {
      restoreJob = await app.api.startRestore({
        source: rsource,
        bases: baseToken ? [{ uploadToken: baseToken }] : undefined,
        passphrase: rpass,
        mode: rmode,
        accounts: rAccounts.length === (inspection.manifest.accounts ?? []).length ? undefined : rAccounts,
        configOnly: rConfigOnly,
        secrets: rSecrets,
        reauth: rSecrets && serverAccount ? buildReauth({ hasPassword: true, password: reauthPassword, secondFactor: reauthCode }) : undefined,
      });
      rstep = 'progress';
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  function restartRestore() {
    rstep = 'source';
    inspection = null;
    restoreJob = null;
    restoreResult = null;
    rsource = {};
    rname = rpath = rpass = baseToken = '';
  }

  function size(n: number): string {
    if (n > 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
    if (n > 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
    return `${Math.max(1, Math.round(n / 1024))} KB`;
  }
</script>

{#snippet reauthFields()}
  <p class="muted">{t('security.stepup.introPassword')}</p>
  <label class="label" for="bk-re-pw">{t('security.stepup.password')}</label>
  <input id="bk-re-pw" class="input" type="password" autocomplete="current-password" bind:value={reauthPassword} />
  <label class="label" for="bk-re-code">{t('security.stepup.code')}</label>
  <input id="bk-re-code" class="input" autocomplete="one-time-code" autocapitalize="off" spellcheck="false" bind:value={reauthCode} />
  <span class="muted">{t('backup.reauthHint')}</span>
{/snippet}

<section class="card wide" id="set-backup" aria-labelledby="set-backup-h">
  <div class="card-head">
    <h2 id="set-backup-h">{t('backup.title')}</h2>
    <div class="seg" role="tablist" aria-label={t('backup.title')}>
      <button role="tab" aria-selected={tab === 'backup'} onclick={() => (tab = 'backup')}><DatabaseBackup size={14} /> {t('backup.tabBackup')}</button>
      <button role="tab" aria-selected={tab === 'restore'} onclick={() => (tab = 'restore')}><RotateCcw size={14} /> {t('backup.tabRestore')}</button>
    </div>
  </div>
  <p class="about">{t('backup.intro')}</p>

  {#if tab === 'backup'}
    <div class="grid">
      <div class="col">
        <h3>{t('backup.scope')}</h3>
        {#each modes as m (m)}
          <label class="radio">
            <input type="radio" bind:group={mode} value={m} />
            <span><strong>{t(`backup.mode.${m}` as MessageKey)}</strong> <span class="muted">{t(`backup.modeHint.${m}` as MessageKey)}</span></span>
          </label>
        {/each}
        {#if mode === 'custom'}
          <ul class="tree" aria-label={t('backup.customTree')}>
            {#each app.accounts as a (a.id)}
              <li><label class="check"><input type="checkbox" checked={selAccounts.includes(a.id)} onchange={() => (selAccounts = toggle(selAccounts, a.id))} /> {a.displayName}</label></li>
            {/each}
            <li class="areas">
              {#each areas as ar (ar)}
                <label class="check"><input type="checkbox" checked={selAreas.includes(ar)} onchange={() => (selAreas = toggle(selAreas, ar))} /> {t(`backup.area.${ar}` as MessageKey)}</label>
              {/each}
            </li>
          </ul>
        {/if}
        {#if !native}
          <label class="check"><input type="checkbox" bind:checked={secrets} /> {t('backup.includeSecrets')}</label>
          {#if secrets && serverAccount}{@render reauthFields()}{/if}
        {/if}
        {#if messengerSelected}<p class="warn"><TriangleAlert size={13} /> {t('backup.messengerEncrypt')}</p>{/if}
      </div>
      <div class="col">
        <h3>{t('backup.encryption')}</h3>
        <label class="label" for="bk-pass">{t('backup.passphrase')}</label>
        <input id="bk-pass" class="input" type="password" autocomplete="new-password" bind:value={pass} />
        <label class="label" for="bk-pass2">{t('backup.passphraseRepeat')}</label>
        <input id="bk-pass2" class="input" type="password" autocomplete="new-password" bind:value={pass2} />
        <p class="hint"><ShieldCheck size={13} /> {needPass ? t('backup.passRequired') : t('backup.passOptional')}</p>
        <h3>{t('backup.target')}</h3>
        <select class="input" bind:value={targetKind} aria-label={t('backup.target')}>
          <option value="download">{t('backup.targetDownload')}</option>
          {#if desktop}<option value="local">{t('backup.targetLocal')}</option>{/if}
          <option value="webdav">{t('backup.targetWebdav')}</option>
        </select>
        {#if targetKind === 'local'}
          <input class="input" bind:value={targetPath} placeholder={t('backup.targetPathHint')} aria-label={t('backup.targetLocal')} />
        {:else if targetKind === 'webdav'}
          <input class="input" bind:value={davUrl} placeholder={t('backup.webdavUrlHint')} aria-label={t('backup.webdavUrl')} />
          <div class="two">
            <input class="input" bind:value={davUser} placeholder={t('backup.webdavUser')} aria-label={t('backup.webdavUser')} />
            <input class="input" type="password" bind:value={davPass} placeholder={t('backup.webdavPassword')} aria-label={t('backup.webdavPassword')} />
          </div>
        {/if}
        <button class="btn btn-primary" disabled={!passOk || (needPass && !pass) || (mode === 'custom' && !selAccounts.length)} onclick={backupNow}><DatabaseBackup size={14} /> {t('backup.now')}</button>
      </div>
    </div>
    {#if backupJob}{#key backupJob.id}<IOJobProgress job={backupJob} />{/key}{/if}

    <div class="card-head sub">
      <h3><CalendarClock size={14} /> {t('backup.schedules')}</h3>
      <button class="btn" onclick={newSchedule}><Plus size={14} /> {t('backup.addSchedule')}</button>
    </div>
    {#if !schedules.length && !editing}<p class="hint">{t('backup.noSchedules')}</p>{/if}
    <ul class="schedules">
      {#each schedules as s (s.id)}
        <li>
          <div class="sinfo">
            <strong>{s.name}</strong>
            <span class="muted">
              {t(`backup.freq.${s.frequency}`)} · {String(s.hour).padStart(2, '0')}:{String(s.minute).padStart(2, '0')} · {t(`backup.mode.${s.scope.mode}` as MessageKey)}
              · {s.target.kind === 'webdav' ? s.target.url : s.target.path} · {t('backup.retention', { daily: s.keepDaily, weekly: s.keepWeekly })}
              {#if s.incremental}· {t('backup.incremental')}{/if}
            </span>
            <span class="muted">
              {#if s.lastRun}{t('backup.lastRun', { when: fmt().dateTime(s.lastRun) })}{/if}
              {#if s.nextRun}· {t('backup.nextRun', { when: fmt().dateTime(s.nextRun) })}{/if}
            </span>
            {#if s.lastError}<span class="errline">{s.lastError}</span>{/if}
          </div>
          <button class="icon-btn" aria-label={t('backup.runNow')} title={t('backup.runNow')} onclick={() => runNow(s.id)}><Play size={15} /></button>
          <button class="icon-btn" aria-label={t('backup.deleteSchedule')} title={t('backup.deleteSchedule')} onclick={() => removeSchedule(s.id)}><Trash size={15} /></button>
        </li>
      {/each}
    </ul>
    {#if schedJob}{#key schedJob.id}<IOJobProgress job={schedJob} onfinished={() => loadSchedules()} />{/key}{/if}
    {#if editing}
      <div class="edit">
        <input class="input" bind:value={editing.name} aria-label={t('backup.scheduleName')} />
        <div class="two">
          <select class="input" bind:value={editing.frequency} aria-label={t('backup.frequency')}>
            <option value="daily">{t('backup.freq.daily')}</option>
            <option value="weekly">{t('backup.freq.weekly')}</option>
          </select>
          <input class="input" type="number" min="0" max="23" bind:value={editing.hour} aria-label={t('backup.hour')} />
        </div>
        <select class="input" bind:value={editing.scope.mode} aria-label={t('backup.scope')}>
          {#each modes.filter((m) => m !== 'custom') as m (m)}<option value={m}>{t(`backup.mode.${m}` as MessageKey)}</option>{/each}
        </select>
        <div class="two">
          <select class="input" bind:value={editing.target.kind} aria-label={t('backup.target')}>
            {#if desktop}<option value="local">{t('backup.targetLocal')}</option>{/if}
            <option value="webdav">{t('backup.targetWebdav')}</option>
          </select>
          {#if editing.target.kind === 'local'}
            <input class="input" bind:value={editing.target.path} placeholder={t('backup.targetPathHint')} aria-label={t('backup.targetLocal')} />
          {:else}
            <input class="input" bind:value={editing.target.url} placeholder={t('backup.webdavUrlHint')} aria-label={t('backup.webdavUrl')} />
          {/if}
        </div>
        {#if editing.target.kind === 'webdav'}
          <div class="two">
            <input class="input" bind:value={editing.target.username} placeholder={t('backup.webdavUser')} aria-label={t('backup.webdavUser')} />
            <input class="input" type="password" bind:value={editing.targetPassword} placeholder={t('backup.webdavPassword')} aria-label={t('backup.webdavPassword')} />
          </div>
        {/if}
        <input class="input" type="password" bind:value={editing.passphrase} placeholder={t('backup.passphrase')} aria-label={t('backup.passphrase')} />
        <div class="two">
          <label class="check"><input type="checkbox" bind:checked={editing.incremental} /> {t('backup.incremental')}</label>
          <span class="muted">{t('backup.retention', { daily: editing.keepDaily, weekly: editing.keepWeekly })}</span>
        </div>
        <div class="nav">
          <button class="btn btn-ghost" onclick={() => (editing = null)}>{t('backup.cancel')}</button>
          <button class="btn btn-primary" onclick={saveSchedule}>{t('backup.saveSchedule')}</button>
        </div>
      </div>
    {/if}
  {:else}
    {#if rstep === 'source'}
      <div class="drop">
        <Upload size={22} />
        <p>{t('backup.restoreDrop')}</p>
        <label class="btn">
          {t('backup.chooseArchive')}
          <input type="file" accept=".txmb" hidden onchange={(e) => { const f = (e.currentTarget as HTMLInputElement).files?.[0]; if (f) uploadArchive(f); }} />
        </label>
        {#if rname}<p class="picked">{rname}</p>{/if}
      </div>
      {#if desktop}
        <input class="input" bind:value={rpath} placeholder={t('backup.archivePath')} aria-label={t('backup.archivePath')} />
      {/if}
      <label class="label" for="rs-pass">{t('backup.passphrase')}</label>
      <input id="rs-pass" class="input" type="password" bind:value={rpass} />
      {#if inspection && !inspection.manifest}<p class="warn">{t('backup.encryptedNeedPass')}</p>{/if}
      <div class="nav"><span></span><button class="btn btn-primary" disabled={busy || (!rsource.uploadToken && !rpath)} onclick={inspect}>{t('backup.inspect')}</button></div>
    {:else if rstep === 'inspect' && inspection?.manifest}
      {@const m = inspection.manifest}
      <dl class="manifest">
        <dt>{t('backup.created')}</dt><dd>{fmt().dateTime(m.created)}</dd>
        <dt>{t('backup.scope')}</dt><dd>{t(`backup.mode.${m.scope.mode}` as MessageKey)}{m.secrets ? ` · ${t('backup.withSecrets')}` : ''}</dd>
        <dt>{t('backup.versions')}</dt><dd>{t('backup.versionInfo', { app: m.appVersion ?? '?', schema: m.schemaVersion })}</dd>
        <dt>{t('backup.content')}</dt><dd>{t('backup.contentInfo', { messages: m.counts?.messages ?? 0, events: m.counts?.events ?? 0, contacts: m.counts?.contacts ?? 0 })}</dd>
      </dl>
      {#if m.baseId}
        <p class="warn">{t('backup.needsBase')}</p>
        <label class="btn">
          {t('backup.chooseBase')}
          <input type="file" accept=".txmb" hidden onchange={(e) => { const f = (e.currentTarget as HTMLInputElement).files?.[0]; if (f) uploadArchive(f, true); }} />
        </label>
      {/if}
      {#if inspection.messenger}<p class="warn"><TriangleAlert size={13} /> {t('backup.messengerWarning')}</p>{/if}
      <h3>{t('backup.selectAccounts')}</h3>
      <ul class="tree">
        {#each m.accounts ?? [] as a (a.id)}
          <li><label class="check"><input type="checkbox" checked={rAccounts.includes(a.id)} onchange={() => (rAccounts = toggle(rAccounts, a.id))} /> {a.name} <span class="muted">{a.address ?? a.kind} · {t('backup.messages', { count: a.messages })}</span></label></li>
        {/each}
      </ul>
      <fieldset>
        <legend>{t('backup.restoreMode')}</legend>
        <label class="radio"><input type="radio" bind:group={rmode} value="merge" /> <span><strong>{t('backup.merge')}</strong> <span class="muted">{t('backup.mergeHint')}</span></span></label>
        <label class="radio"><input type="radio" bind:group={rmode} value="replace" /> <span><strong>{t('backup.replace')}</strong> <span class="muted">{t('backup.replaceHint')}</span></span></label>
        <label class="check"><input type="checkbox" bind:checked={rConfigOnly} /> {t('backup.configOnly')}</label>
        {#if m.secrets && !native}
          <label class="check"><input type="checkbox" bind:checked={rSecrets} /> {t('backup.restoreSecrets')}</label>
          {#if rSecrets && serverAccount}{@render reauthFields()}{/if}
        {/if}
      </fieldset>
      <div class="nav">
        <button class="btn btn-ghost" onclick={restartRestore}>{t('backup.back')}</button>
        <button class="btn btn-primary" disabled={!rAccounts.length && !rConfigOnly} onclick={restore}><RotateCcw size={14} /> {t('backup.restore')}</button>
      </div>
    {:else if rstep === 'progress' && restoreJob}
      <IOJobProgress job={restoreJob} onfinished={(j) => (restoreResult = (j.result as RestoreResult) ?? null)} />
      {#if restoreResult?.restartRequired}<p class="warn">{t('backup.restartRequired')}</p>{/if}
      {#if restoreResult && restoreResult.schemaFrom !== restoreResult.schemaTo}<p class="hint">{t('backup.migrated', { from: restoreResult.schemaFrom, to: restoreResult.schemaTo })}</p>{/if}
      <div class="nav"><span></span><button class="btn" onclick={restartRestore}>{t('backup.done')}</button></div>
    {/if}
    {#if schedules.length}
      <p class="hint">{t('backup.archivesHint')}</p>
      {#each schedules as s (s.id)}
        {#await app.api.listBackupArchives(s.id) then archives}
          <ul class="schedules">
            {#each archives as ar (ar.name)}
              <li>
                <div class="sinfo"><strong>{ar.name}</strong><span class="muted">{s.name} · {size(ar.size)} · {fmt().dateTime(ar.modified)}</span></div>
                <button class="btn btn-ghost" onclick={() => { rsource = { scheduleId: s.id, name: ar.name }; rname = ar.name; rpath = ''; }}>{t('backup.useArchive')}</button>
              </li>
            {/each}
          </ul>
        {:catch}
          <span></span>
        {/await}
      {/each}
    {/if}
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
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
  .card-head.sub {
    margin-top: 8px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  h3 {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 4px 0 0;
    font-size: 12.5px;
    font-weight: 650;
    color: var(--tm-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .about,
  .hint {
    display: flex;
    align-items: center;
    gap: 5px;
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
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
    gap: 18px;
  }
  .col {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .check,
  .radio {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    font-size: 13px;
  }
  .radio input,
  .check input {
    margin-top: 3px;
  }
  .muted {
    color: var(--tm-muted);
    font-size: 12.5px;
  }
  .tree {
    list-style: none;
    margin: 0 0 0 22px;
    padding: 6px 10px;
    border-left: 2px solid var(--tm-line);
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .areas {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 14px;
    padding-top: 4px;
    border-top: 1px dashed var(--tm-line);
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 6px;
    align-items: center;
  }
  .warn {
    display: flex;
    align-items: center;
    gap: 5px;
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-warn);
  }
  .err,
  .errline {
    margin: 0;
    color: var(--tm-danger);
    font-size: 12.5px;
  }
  .schedules {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .schedules li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 0;
    border-bottom: 1px solid var(--tm-line);
  }
  .sinfo {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 13px;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .edit {
    display: flex;
    flex-direction: column;
    gap: 6px;
    padding: 12px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-bg);
  }
  .nav {
    display: flex;
    justify-content: space-between;
    gap: 8px;
  }
  .drop {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    padding: 22px;
    border: 2px dashed var(--tm-line-strong);
    border-radius: var(--tm-radius);
    color: var(--tm-muted);
    text-align: center;
  }
  .drop p {
    margin: 0;
    font-size: 13px;
  }
  .picked {
    color: var(--tm-success);
    font-weight: 600;
  }
  .manifest {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 4px 14px;
    margin: 0;
    font-size: 13px;
  }
  .manifest dt {
    color: var(--tm-muted);
  }
  .manifest dd {
    margin: 0;
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
  label.btn {
    align-self: flex-start;
  }
</style>
