<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings -> "Serverregeln (Sieve)" (KONZEPT 8.5): ManageSieve scripts of an
  IMAP account, a raw editor with syntax highlighting and server check, and
  the toggle that transfers server-representable workflows.
-->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import LoaderCircle from '@lucide/svelte/icons/loader-circle';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { highlightSieve, lintSieve } from '../../sieve';
  import type { SieveCheckResult, SieveConfig, SieveStatus, TLSMode } from '../../types';

  const accounts = $derived(app.accounts.filter((a) => a.kind === 'imap'));
  let accountId = $state<number | null>(null);
  let status = $state<SieveStatus | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);

  let name = $state('');
  let script = $state('');
  let active = $state(false);
  let dirty = $state(false);
  let check = $state<SieveCheckResult | null>(null);
  let busy = $state(false);
  let scroller = $state<HTMLPreElement | null>(null);

  $effect(() => {
    if (accountId === null && accounts.length) accountId = accounts[0].id;
  });
  $effect(() => {
    if (accountId !== null) void load(accountId);
  });

  const lint = $derived(lintSieve(script));
  const highlighted = $derived(highlightSieve(script) + '\n');

  function msg(err: unknown): string {
    return err instanceof Error ? err.message : String(err);
  }

  let config = $state<SieveConfig | null>(null);
  let configMsg = $state<string | null>(null);
  const tlsModes: TLSMode[] = ['starttls', 'tls', 'none'];

  async function saveConfig() {
    if (accountId === null || !config) return;
    configMsg = null;
    try {
      config = await app.api.setSieveConfig(accountId, { ...config, port: Number(config.port) || 4190 });
      configMsg = t('settings.sieve.configSaved');
      await load(accountId);
    } catch (err) {
      configMsg = msg(err);
    }
  }

  async function load(id: number) {
    loading = true;
    error = null;
    try {
      config = await app.api.getSieveConfig(id).catch(() => null);
      const st = await app.api.getSieveStatus(id);
      // A server or an offline stand-in may leave the script list out: treat that as "no scripts".
      status = { ...st, scripts: Array.isArray(st?.scripts) ? st.scripts : [] };
      const first = status.scripts.find((s) => s.active) ?? status.scripts[0];
      if (first) await open(first.name);
      else {
        name = '';
        script = '';
      }
    } catch (err) {
      // Programming-error texts ("Cannot read properties of undefined") never reach the user.
      error = err instanceof TypeError ? t('connection.notAvailableOffline') : msg(err);
    } finally {
      loading = false;
    }
  }

  async function open(n: string) {
    if (accountId === null) return;
    try {
      const s = await app.api.getSieveScript(accountId, n);
      name = s.name;
      script = s.script;
      active = s.active;
      dirty = false;
      check = null;
    } catch (err) {
      error = msg(err);
    }
  }

  function newScript() {
    name = 'neu';
    script = 'require ["fileinto"];\n\n';
    active = false;
    dirty = true;
    check = null;
  }

  async function runCheck() {
    if (accountId === null) return;
    busy = true;
    try {
      check = await app.api.checkSieveScript(accountId, script);
    } catch (err) {
      check = { ok: false, error: msg(err) };
    } finally {
      busy = false;
    }
  }

  async function save(activate: boolean) {
    if (accountId === null || !name.trim()) return;
    busy = true;
    try {
      await app.api.putSieveScript(accountId, name.trim(), script, activate);
      dirty = false;
      check = { ok: true };
      status = await app.api.getSieveStatus(accountId);
      active = activate || active;
    } catch (err) {
      check = { ok: false, error: msg(err) };
    } finally {
      busy = false;
    }
  }

  async function activate(n: string) {
    if (accountId === null) return;
    await app.api.activateSieveScript(accountId, n);
    status = await app.api.getSieveStatus(accountId);
    active = n === name;
  }

  async function remove(n: string) {
    if (accountId === null) return;
    try {
      await app.api.deleteSieveScript(accountId, n);
      status = await app.api.getSieveStatus(accountId);
      if (n === name) {
        name = '';
        script = '';
      }
    } catch (err) {
      error = msg(err);
    }
  }

  async function toggleSync(on: boolean) {
    if (accountId === null) return;
    busy = true;
    try {
      status = await app.api.setSieveWorkflowSync(accountId, on);
      const cur = (status.scripts ?? []).find((s) => s.name === name) ?? (status.scripts ?? []).find((s) => s.active);
      if (cur && !dirty) await open(cur.name);
    } catch (err) {
      error = msg(err);
    } finally {
      busy = false;
    }
  }

  function syncScroll(e: Event) {
    const ta = e.currentTarget as HTMLTextAreaElement;
    if (scroller) {
      scroller.scrollTop = ta.scrollTop;
      scroller.scrollLeft = ta.scrollLeft;
    }
  }
</script>

<section class="card wide-card" id="set-sieve" aria-labelledby="set-sieve-h">
  <h2 id="set-sieve-h">{t('settings.sieve.title')}</h2>
  <p class="about">{t('settings.sieve.intro')}</p>
  {#if !accounts.length}
    <p class="hint">{t('settings.sieve.noAccounts')}</p>
  {:else}
    <div class="row">
      <label>
        <span class="label">{t('settings.sieve.account')}</span>
        <select class="input" bind:value={accountId}>
          {#each accounts as a (a.id)}<option value={a.id}>{a.displayName}</option>{/each}
        </select>
      </label>
      {#if loading}<LoaderCircle size={16} class="spin" />{/if}
    </div>
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#if config}
      <details class="advanced">
        <summary>{t('settings.sieve.advanced')}</summary>
        <div class="cfg">
          <label class="wide"><span class="label">{t('onboarding.account.host')}</span><input class="input" bind:value={config.host} /></label>
          <label><span class="label">{t('onboarding.account.port')}</span><input class="input" type="number" min="1" max="65535" bind:value={config.port} /></label>
          <label>
            <span class="label">{t('onboarding.account.tls')}</span>
            <select class="input" bind:value={config.tls}>
              {#each tlsModes as m (m)}<option value={m}>{t(`settings.tls.${m}`)}</option>{/each}
            </select>
          </label>
        </div>
        <label class="check"><input type="checkbox" bind:checked={config.disabled} /> {t('settings.sieve.disabled')}</label>
        <label class="check"><input type="checkbox" bind:checked={config.separate} /> {t('settings.sieve.separate')}</label>
        <div class="actions">
          <button class="btn" onclick={saveConfig}>{t('common.action.save')}</button>
          {#if configMsg}<span class="hint">{configMsg}</span>{/if}
        </div>
      </details>
    {/if}
    {#if status && !status.available}
      <p class="warn"><TriangleAlert size={15} /> {t('settings.sieve.unavailable')}{status.error ? `: ${status.error}` : ''}</p>
    {:else if status}
      <label class="check">
        <input type="checkbox" checked={status.syncWorkflows} disabled={busy} onchange={(e) => toggleSync((e.currentTarget as HTMLInputElement).checked)} />
        {t('settings.sieve.syncWorkflows')}
      </label>
      {#if status.included.length || status.skipped.length}
        <details class="preview">
          <summary>{t('settings.sieve.preview', { included: status.included.length, skipped: status.skipped.length })}</summary>
          <ul>
            {#each status.included as id (id)}<li class="ok"><CircleCheck size={13} /> {id}</li>{/each}
            {#each status.skipped as s (s.id)}<li class="skip" title={s.reason}><TriangleAlert size={13} /> {s.name}: {s.reason}</li>{/each}
          </ul>
        </details>
      {/if}

      <div class="layout">
        <div class="scripts">
          <div class="head">
            <span class="label">{t('settings.sieve.scripts')}</span>
            <button class="icon-btn" aria-label={t('settings.sieve.new')} title={t('settings.sieve.new')} onclick={newScript}><Plus size={15} /></button>
          </div>
          <ul>
            {#each (status.scripts ?? []) as s (s.name)}
              <li class:sel={s.name === name}>
                <button class="name" onclick={() => open(s.name)}>{s.name}</button>
                {#if s.active}<span class="badge">{t('settings.sieve.active')}</span>{:else}
                  <button class="btn btn-ghost small" onclick={() => activate(s.name)}>{t('settings.sieve.activate')}</button>
                  <button class="icon-btn" aria-label={t('settings.sieve.delete', { name: s.name })} onclick={() => remove(s.name)}><Trash size={14} /></button>
                {/if}
              </li>
            {/each}
          </ul>
        </div>
        <div class="editor">
          <label class="nm"><span class="label">{t('settings.sieve.name')}</span><input class="input" bind:value={name} oninput={() => (dirty = true)} /></label>
          <div class="code">
            <pre class="hl" aria-hidden="true" bind:this={scroller}>{@html highlighted}</pre>
            <textarea
              class="src"
              spellcheck="false"
              aria-label={t('settings.sieve.editor')}
              bind:value={script}
              oninput={() => ((dirty = true), (check = null))}
              onscroll={syncScroll}
            ></textarea>
          </div>
          {#if lint.error}<p class="error">{t('settings.sieve.lint', { line: lint.line ?? 0, error: lint.error })}</p>{/if}
          {#if check}
            {#if check.ok}<p class="okmsg"><CircleCheck size={14} /> {check.warnings || t('settings.sieve.checkOk')}</p>
            {:else}<p class="error" role="alert">{check.error}</p>{/if}
          {/if}
          <div class="actions">
            <button class="btn" disabled={busy} onclick={runCheck}>{t('settings.sieve.check')}</button>
            <button class="btn" disabled={busy || !name.trim()} onclick={() => save(false)}>{t('common.action.save')}</button>
            {#if !active}<button class="btn btn-primary" disabled={busy || !name.trim()} onclick={() => save(true)}>{t('settings.sieve.saveActivate')}</button>{/if}
          </div>
          <p class="hint">{t('settings.sieve.managedHint')}</p>
        </div>
      </div>
    {/if}
  {/if}
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .wide-card {
    grid-column: 1 / -1;
  }
  h2 {
    margin: 0 0 10px;
    font-size: 15px;
    font-weight: 650;
  }
  .about,
  .hint {
    margin: 0 0 10px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .row {
    display: flex;
    align-items: flex-end;
    gap: 10px;
    margin-bottom: 10px;
  }
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    font-size: 13px;
    margin: 6px 0 10px;
  }
  .warn,
  .error {
    display: flex;
    gap: 6px;
    align-items: center;
    color: var(--tm-danger, #b42318);
    font-size: 13px;
    margin: 4px 0;
  }
  .okmsg {
    display: flex;
    gap: 6px;
    align-items: center;
    color: var(--tm-success);
    font-size: 13px;
    margin: 4px 0;
  }
  .advanced {
    font-size: 13px;
    margin-bottom: 10px;
  }
  .cfg {
    display: grid;
    grid-template-columns: 2fr 1fr 1fr;
    gap: 8px;
    margin: 8px 0;
  }
  .preview {
    font-size: 12.5px;
    margin-bottom: 12px;
  }
  .preview ul {
    list-style: none;
    padding: 4px 0 0;
    margin: 0;
  }
  .preview li {
    display: flex;
    gap: 6px;
    align-items: center;
    padding: 2px 0;
  }
  .preview .ok {
    color: var(--tm-success);
  }
  .preview .skip {
    color: var(--tm-muted);
  }
  .layout {
    display: grid;
    grid-template-columns: 220px 1fr;
    gap: 16px;
  }
  .scripts .head {
    display: flex;
    justify-content: space-between;
    align-items: center;
  }
  .scripts ul {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .scripts li {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 4px 6px;
    border-radius: 8px;
  }
  .scripts li.sel {
    background: var(--tm-surface-3);
  }
  .name {
    flex: 1;
    min-width: 0;
    text-align: left;
    border: none;
    background: none;
    font: inherit;
    font-family: var(--tm-mono);
    font-size: 12.5px;
    cursor: pointer;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .badge {
    font-size: 11px;
    padding: 1px 8px;
    border-radius: 99px;
    background: color-mix(in srgb, var(--tm-success) 16%, var(--tm-surface));
    color: var(--tm-success);
  }
  .small {
    height: 24px;
    padding: 0 6px;
    font-size: 12px;
  }
  .nm {
    display: block;
    margin-bottom: 8px;
  }
  .code {
    position: relative;
    height: 340px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface-2, var(--tm-surface));
    overflow: hidden;
  }
  .hl,
  .src {
    position: absolute;
    inset: 0;
    margin: 0;
    padding: 10px 12px;
    font-family: var(--tm-mono);
    font-size: 12.5px;
    line-height: 1.5;
    white-space: pre;
    tab-size: 2;
    overflow: auto;
    box-sizing: border-box;
  }
  .hl {
    pointer-events: none;
    color: var(--tm-fg);
  }
  .src {
    width: 100%;
    height: 100%;
    border: 0;
    resize: none;
    background: transparent;
    color: transparent;
    caret-color: var(--tm-fg);
    outline: none;
  }
  .hl :global(.sv-comment) {
    color: var(--tm-muted);
    font-style: italic;
  }
  .hl :global(.sv-string) {
    color: #0a7d32;
  }
  .hl :global(.sv-keyword) {
    color: #8250df;
    font-weight: 600;
  }
  .hl :global(.sv-tag) {
    color: #0550ae;
  }
  .hl :global(.sv-number) {
    color: #953800;
  }
  .hl :global(.sv-error) {
    color: #cf222e;
    text-decoration: underline wavy;
  }
  .actions {
    display: flex;
    gap: 8px;
    margin: 10px 0;
  }
  @media (max-width: 799px) {
    .layout {
      grid-template-columns: 1fr;
    }
  }

  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .name {
      min-height: 44px;
      font-size: 14px;
    }
  }
</style>
