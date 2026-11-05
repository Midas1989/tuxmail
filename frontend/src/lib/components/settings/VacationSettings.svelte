<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings -> "Abwesenheitsnotiz" (KONZEPT 8.5): out-of-office reply. Pushed to
  mail servers that can answer themselves (Sieve, JMAP, Exchange, Gmail);
  other accounts are answered by TuxMail once per sender.
-->
<script lang="ts">
  import Server from '@lucide/svelte/icons/server';
  import { app } from '../../state';
  import { ApiError } from '../../api/api';
  import ConfirmDialog from '../common/ConfirmDialog.svelte';
  import { t } from '../../i18n/index.svelte';
  import type { VacationSettings } from '../../types';

  let s = $state<VacationSettings>({ enabled: false });
  let start = $state('');
  let end = $state('');
  let ownDomains = $state('');
  let forwardTo = $state('');
  let error = $state<string | null>(null);
  let saved = $state(false);
  let busy = $state(false);
  let confirmForward = $state(false);

  /** RFC 3339 -> value of <input type="datetime-local"> (local time); zero dates are empty. */
  function toLocalInput(v?: string | null): string {
    if (!v) return '';
    const d = new Date(v);
    if (isNaN(d.getTime()) || d.getFullYear() < 1000) return '';
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
  }
  function fromLocalInput(v: string): string | null {
    return v ? new Date(v).toISOString() : null;
  }
  const list = (v: string) =>
    v
      .split(/[,\s]+/)
      .map((x) => x.trim())
      .filter(Boolean);

  async function load() {
    try {
      s = await app.api.getVacationSettings();
      start = toLocalInput(s.start);
      end = toLocalInput(s.end);
      ownDomains = (s.ownDomains ?? []).join(', ');
      forwardTo = (s.forwardTo ?? []).join(', ');
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }
  void load();

  async function save(confirmed = false) {
    busy = true;
    error = null;
    saved = false;
    try {
      const { serverAccounts: _ignored, ...rest } = s;
      s = await app.api.saveVacationSettings({
        ...rest,
        start: fromLocalInput(start),
        end: fromLocalInput(end),
        ownDomains: list(ownDomains),
        forwardTo: list(forwardTo),
        oncePerDays: Math.max(0, Number(rest.oncePerDays) || 0),
        confirmExternal: confirmed || undefined,
      });
      saved = true;
    } catch (err) {
      if (err instanceof ApiError && err.code === 'confirmation_required' && !confirmed) {
        confirmForward = true; // forwarding is new or changed: ask first
        return;
      }
      error = err instanceof Error ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  const serverNames = $derived((s.serverAccounts ?? []).map((id) => app.account(id)?.displayName ?? `#${id}`));
</script>

<section class="card" id="set-vacation" aria-labelledby="set-vacation-h">
  <h2 id="set-vacation-h">{t('settings.vacation.title')}</h2>
  <p class="hint">{t('settings.vacation.intro')}</p>
  <form
    onsubmit={(e) => {
      e.preventDefault();
      void save();
    }}
  >
    <label class="check"><input type="checkbox" bind:checked={s.enabled} /> {t('settings.vacation.enabled')}</label>
    <div class="grid">
      <label><span class="label">{t('settings.vacation.start')}</span><input class="input" type="datetime-local" bind:value={start} /></label>
      <label><span class="label">{t('settings.vacation.end')}</span><input class="input" type="datetime-local" bind:value={end} /></label>
    </div>
    <label><span class="label">{t('settings.vacation.subject')}</span><input class="input" bind:value={s.subject} placeholder={t('settings.vacation.subjectDefault')} /></label>
    <label><span class="label">{t('settings.vacation.externalText')}</span><textarea class="input text" rows="4" bind:value={s.externalText}></textarea></label>
    <label><span class="label">{t('settings.vacation.internalText')}</span><textarea class="input text" rows="3" bind:value={s.internalText}></textarea></label>
    <div class="grid">
      <label><span class="label">{t('settings.vacation.ownDomains')}</span><input class="input" bind:value={ownDomains} /></label>
      <label><span class="label">{t('settings.vacation.oncePerDays')}</span><input class="input" type="number" min="0" max="365" bind:value={s.oncePerDays} /></label>
    </div>
    <label><span class="label">{t('settings.vacation.forwardTo')}</span><input class="input" bind:value={forwardTo} /></label>
    <p class="hint">{t('settings.vacation.placeholders')}</p>
    {#if s.enabled && serverNames.length}
      <p class="server"><Server size={14} /> {t('settings.vacation.serverHandled', { accounts: serverNames.join(', ') })}</p>
    {/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    {#if saved}<p class="ok">{t('settings.vacation.saved')}</p>{/if}
    <div class="actions"><button class="btn btn-primary" type="submit" disabled={busy}>{t('common.action.save')}</button></div>
  </form>
</section>
{#if confirmForward}
  <ConfirmDialog
    title={t('settings.vacation.confirmForward.title')}
    message={t('settings.vacation.confirmForward.text', { addresses: list(forwardTo).join(', ') })}
    confirmLabel={t('settings.vacation.confirmForward.ok')}
    onconfirm={() => {
      confirmForward = false;
      void save(true);
    }}
    oncancel={() => (confirmForward = false)}
  />
{/if}

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  h2 {
    margin: 0 0 10px;
    font-size: 15px;
    font-weight: 650;
  }
  form {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    font-size: 13px;
  }
  .text {
    height: auto;
    padding: 6px 10px;
    resize: vertical;
  }
  .hint {
    margin: 0 0 6px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .server {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 12.5px;
    margin: 0;
  }
  .error {
    color: var(--tm-danger, #b42318);
    font-size: 13px;
    margin: 0;
  }
  .ok {
    color: var(--tm-success);
    font-size: 13px;
    margin: 0;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
  }
</style>
