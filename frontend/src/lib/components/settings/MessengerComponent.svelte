<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  External program of a messenger plugin (Signal: signal-cli): state, the
  on-demand install (pinned, checksum-verified download) with progress.
  The parent reloads the plugin on messenger.plugin events.
-->
<script lang="ts">
  import Download from '@lucide/svelte/icons/download';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { MessengerComponent, MessengerPlugin } from '../../types';

  interface Props {
    plugin: MessengerPlugin;
    /** Compact line when the component is ready (settings list). */
    compact?: boolean;
    /** Text below the install button (e.g. "linking starts again"). */
    extra?: string;
  }
  let { plugin, compact = false, extra }: Props = $props();

  let error = $state<string | null>(null);
  let busy = $state(false);
  const c = $derived<MessengerComponent | undefined>(plugin.component);
  const percent = $derived(c?.progress && c.progress.total > 0 ? Math.min(100, Math.round((c.progress.done / c.progress.total) * 100)) : 0);

  const stageText = $derived.by(() => {
    const p = c?.progress;
    if (!p) return t('messenger.component.installing');
    if (p.stage === 'download') return t('messenger.component.stage.download', { file: p.file ?? '', percent });
    return t(`messenger.component.stage.${p.stage}` as 'messenger.component.stage.verify');
  });

  async function install() {
    busy = true;
    error = null;
    try {
      await app.api.installMessengerComponent(plugin.name);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

{#if c}
  <div class="comp st-{c.state}" class:compact data-component={c.name} data-state={c.state} aria-live="polite">
    {#if c.state === 'ready'}
      <p class="line ok">
        <CircleCheck size={14} />
        {c.version ? t('messenger.component.ready', { version: c.version }) : t('messenger.component.readyNoVersion')}
        {#if c.source}<span class="sub">· {t(`messenger.component.source.${c.source}` as 'messenger.component.source.bundled')}</span>{/if}
      </p>
    {:else if c.state === 'installing'}
      <p class="line">{t('messenger.component.installing')}</p>
      <div class="bar" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow={percent} aria-label={t('messenger.component.title')}>
        <span style:width="{c.progress?.stage === 'download' ? percent : 100}%"></span>
      </div>
      <p class="sub">{stageText}</p>
    {:else}
      <p class="line warn"><TriangleAlert size={14} /> {t('messenger.component.missing')}</p>
      {#if c.state === 'failed' && c.error}<p class="sub err">{t('messenger.component.failed', { error: c.error })}</p>{/if}
      {#if c.installable}
        <p class="sub">{t('messenger.component.missingText', { version: c.installVersion ?? '' })}</p>
        {#if c.note}<p class="sub">{t('messenger.component.note', { note: c.note })}</p>{/if}
        <button class="btn btn-primary" onclick={install} disabled={busy} data-testid="install-component"><Download size={14} /> {t('messenger.component.install')}</button>
        {#if extra}<p class="sub">{extra}</p>{/if}
      {:else}
        <p class="sub">{t('messenger.component.manual')}</p>
      {/if}
    {/if}
    {#if error}<p class="sub err" role="alert">{error}</p>{/if}
  </div>
{/if}

<style>
  .comp {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin: 10px 0 0;
    padding: 10px 12px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    font-size: 13px;
  }
  .comp.compact.st-ready {
    border: 0;
    padding: 0;
    margin-top: 4px;
  }
  .comp:not(.st-ready):not(.st-installing) {
    border-color: var(--tm-warning, #b7791f);
    background: color-mix(in srgb, var(--tm-warning, #b7791f) 8%, transparent);
  }
  .line {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-weight: 600;
  }
  .line.ok {
    color: var(--tm-success);
    font-weight: 500;
    font-size: 12.5px;
  }
  .sub {
    margin: 0;
    font-size: 12.5px;
    color: var(--tm-muted);
    font-weight: 400;
  }
  .err {
    color: var(--tm-danger, #c53030);
  }
  .btn {
    align-self: flex-start;
    display: inline-flex;
    gap: 6px;
    align-items: center;
  }
  .bar {
    height: 6px;
    border-radius: 999px;
    background: var(--tm-line);
    overflow: hidden;
  }
  .bar span {
    display: block;
    height: 100%;
    background: var(--tm-accent);
    transition: width 0.2s;
  }
</style>
