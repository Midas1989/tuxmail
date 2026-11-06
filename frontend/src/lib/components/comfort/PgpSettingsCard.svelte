<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> OpenPGP key manager: create, import, export, trust and delete keys; Autocrypt; defaults. -->
<script lang="ts">
  import Upload from '@lucide/svelte/icons/upload';
  import Trash from '@lucide/svelte/icons/trash';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import Download from '@lucide/svelte/icons/download';
  import Lock from '@lucide/svelte/icons/lock';
  import PgpUnlockDialog from './PgpUnlockDialog.svelte';
  import { app, comfort } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import { ApiError } from '../../api/api';
  import type { MessageKey } from '../../i18n/index.svelte';
  import type { ComfortSettings, PgpKey, PgpPeer } from '../../api/comfort';

  let keys = $state<PgpKey[]>([]);
  let peers = $state<PgpPeer[]>([]);
  let fileInput = $state<HTMLInputElement>();
  let pending = $state<File | null>(null);
  let passphrase = $state('');
  let error = $state<string | null>(null);
  let s = $state<ComfortSettings | null>(null);
  let busy = $state(false);
  let genEmail = $state('');
  let genName = $state('');
  let exporting = $state<PgpKey | null>(null);
  let exportPass = $state('');
  let exportCurrent = $state('');
  let genPass = $state('');
  let unlocking = $state<PgpKey | null>(null);
  let changing = $state<PgpKey | null>(null);
  let changeCurrent = $state('');
  let changeNew = $state('');
  let lookupEmail = $state('');

  async function load() {
    keys = await comfort.api.listPgpKeys().catch(() => []);
    peers = await comfort.api.listPgpPeers().catch(() => []);
  }
  $effect(() => {
    void load();
    void comfort.loadSettings().then((v) => v && (s = structuredClone($state.snapshot(v) as ComfortSettings)));
  });

  const codes = ['pgp_import', 'pgp_wrong_passphrase', 'pgp_unlock_throttled', 'pgp_passphrase_short', 'pgp_key_not_found', 'pgp_lookup_failed', 'pgp_key_changed'];
  function show(e: unknown) {
    if (e instanceof ApiError && codes.includes(e.code)) error = t(`comfort.pgp.err.${e.code}` as MessageKey);
    else error = e instanceof Error ? e.message : String(e);
  }

  function fingerprint(f: string): string {
    return f.replace(/(.{4})/g, '$1 ').trim();
  }

  async function generate() {
    error = null;
    busy = true;
    try {
      const k = await comfort.api.generatePgpKey({ name: genName, email: genEmail, passphrase: genPass || undefined });
      app.toast(t('comfort.pgp.generated', { email: k.email }));
      genEmail = genName = genPass = '';
      await load();
    } catch (e) {
      show(e);
    } finally {
      busy = false;
    }
  }

  async function doImport(f: File, pass: string) {
    error = null;
    try {
      const out = await comfort.api.importPgpKeys(f, pass || undefined);
      app.toast(t('comfort.pgp.imported', { count: out.length }));
      pending = null;
      passphrase = '';
      await load();
    } catch (e) {
      show(e);
    }
  }

  function download(name: string, text: string) {
    const url = URL.createObjectURL(new Blob([text], { type: 'application/pgp-keys' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    a.click();
    URL.revokeObjectURL(url);
  }

  async function exportPublic(k: PgpKey) {
    error = null;
    try {
      download(`${k.email || k.fingerprint}.pub.asc`, await comfort.api.exportPgpPublicKey(k.id));
    } catch (e) {
      show(e);
    }
  }

  async function exportSecret() {
    if (!exporting) return;
    error = null;
    try {
      download(`${exporting.email || exporting.fingerprint}.private.asc`, await comfort.api.exportPgpSecretKey(exporting.id, exportPass, exportCurrent || undefined));
      exporting = null;
      exportPass = exportCurrent = '';
    } catch (e) {
      show(e);
    }
  }

  async function changePassphrase() {
    if (!changing) return;
    error = null;
    try {
      await comfort.api.changePgpPassphrase(changing.id, changeCurrent, changeNew);
      app.toast(t(changeNew ? 'comfort.pgp.passphraseSet' : 'comfort.pgp.passphraseRemoved'));
      changing = null;
      changeCurrent = changeNew = '';
      await load();
    } catch (e) {
      changeCurrent = '';
      show(e);
    }
  }

  async function lockAll() {
    await comfort.api.lockPgpKeys();
    app.toast(t('comfort.pgp.lockedAll'));
    await load();
  }

  async function trust(k: PgpKey) {
    const verified = k.trust !== 'verified';
    if (verified && !confirm(t('comfort.pgp.confirmVerify', { fingerprint: fingerprint(k.fingerprint) }))) return;
    await comfort.api.verifyPgpKey(k.id, verified);
    await load();
  }

  async function del(k: PgpKey) {
    if (!confirm(t(k.own ? 'comfort.pgp.confirmDeleteOwn' : 'comfort.pgp.confirmDelete', { email: k.email }))) return;
    await comfort.api.deletePgpKey(k.id);
    await load();
  }

  async function verifyPeer(p: PgpPeer) {
    if (!confirm(t('comfort.pgp.confirmVerify', { fingerprint: fingerprint(p.fingerprint) }))) return;
    error = null;
    try {
      await comfort.api.verifyPgpPeer(p.address, p.fingerprint);
      await load();
    } catch (e) {
      show(e);
    }
  }

  async function delPeer(p: PgpPeer) {
    if (!confirm(t('comfort.pgp.confirmDelete', { email: p.address }))) return;
    await comfort.api.deletePgpPeer(p.address);
    await load();
  }

  async function lookup(source: 'wkd' | 'keyserver') {
    error = null;
    if (!confirm(t(`comfort.pgp.lookupConfirm.${source}`, { email: lookupEmail }))) return;
    busy = true;
    try {
      const k = await comfort.api.lookupPgpKey(lookupEmail, source);
      app.toast(t('comfort.pgp.found', { email: k.email }));
      lookupEmail = '';
      await load();
    } catch (e) {
      show(e);
    } finally {
      busy = false;
    }
  }

  async function saveDefaults() {
    if (s) await comfort.saveSettings($state.snapshot(s) as ComfortSettings);
    app.toast(t('notifications.saved'));
  }
</script>

<section class="card" id="set-pgp" aria-labelledby="set-pgp-h">
  <div class="head">
    <h2 id="set-pgp-h">{t('comfort.pgp.title')}</h2>
    <span class="head-acts">
      {#if keys.some((k) => k.protected && !k.locked)}<button class="btn btn-ghost" onclick={lockAll}><Lock size={15} /> {t('comfort.pgp.lockAll')}</button>{/if}
      <button class="btn" onclick={() => fileInput?.click()}><Upload size={15} /> {t('comfort.pgp.import')}</button>
    </span>
    <input type="file" accept=".asc,.gpg,.pgp,.key" hidden bind:this={fileInput} onchange={(e) => { pending = (e.currentTarget as HTMLInputElement).files?.[0] ?? null; passphrase = ''; error = null; }} />
  </div>
  <p class="hint">{t('comfort.pgp.intro')}</p>

  <form class="row" onsubmit={(e) => { e.preventDefault(); void generate(); }}>
    <input class="input" list="pgp-identities" type="email" required bind:value={genEmail} placeholder={t('comfort.pgp.email')} aria-label={t('comfort.pgp.email')} />
    <datalist id="pgp-identities">
      {#each app.identities as i (i.id)}<option value={i.email}></option>{/each}
    </datalist>
    <input class="input" bind:value={genName} placeholder={t('comfort.pgp.name')} aria-label={t('comfort.pgp.name')} />
    <input class="input" type="password" autocomplete="new-password" bind:value={genPass} placeholder={t('comfort.pgp.newPassphraseOptional')} aria-label={t('comfort.pgp.newPassphraseOptional')} />
    <button class="btn btn-primary" disabled={busy || !genEmail}>{t('comfort.pgp.generate')}</button>
  </form>
  <p class="hint">{t('comfort.pgp.passphraseHint')}</p>

  {#if pending}
    <form class="row" onsubmit={(e) => { e.preventDefault(); if (pending) void doImport(pending, passphrase); }}>
      <KeyRound size={15} aria-hidden="true" />
      <span>{pending.name}</span>
      <input class="input" type="password" autocomplete="off" bind:value={passphrase} placeholder={t('comfort.pgp.importPassphrase')} aria-label={t('comfort.pgp.importPassphrase')} />
      <button class="btn btn-primary">{t('comfort.pgp.importNow')}</button>
      <button type="button" class="btn btn-ghost" onclick={() => { pending = null; passphrase = ''; }}>{t('common.action.cancel')}</button>
    </form>
  {/if}
  {#if exporting}
    <form class="row" onsubmit={(e) => { e.preventDefault(); void exportSecret(); }}>
      <KeyRound size={15} aria-hidden="true" />
      <span>{exporting.email}</span>
      {#if exporting.protected}<input class="input" type="password" autocomplete="off" bind:value={exportCurrent} placeholder={t('comfort.pgp.currentPassphrase')} aria-label={t('comfort.pgp.currentPassphrase')} />{/if}
      <input class="input" type="password" autocomplete="new-password" minlength="12" bind:value={exportPass} placeholder={t('comfort.pgp.exportPassphrase')} aria-label={t('comfort.pgp.exportPassphrase')} />
      <button class="btn btn-primary">{t('comfort.pgp.exportSecret')}</button>
      <button type="button" class="btn btn-ghost" onclick={() => { exporting = null; exportPass = exportCurrent = ''; }}>{t('common.action.cancel')}</button>
    </form>
  {/if}
  {#if changing}
    <form class="row" onsubmit={(e) => { e.preventDefault(); void changePassphrase(); }}>
      <KeyRound size={15} aria-hidden="true" />
      <span>{changing.email}</span>
      {#if changing.protected}<input class="input" type="password" autocomplete="off" bind:value={changeCurrent} placeholder={t('comfort.pgp.currentPassphrase')} aria-label={t('comfort.pgp.currentPassphrase')} />{/if}
      <input class="input" type="password" autocomplete="new-password" bind:value={changeNew} placeholder={t('comfort.pgp.newPassphraseEmpty')} aria-label={t('comfort.pgp.newPassphraseEmpty')} />
      <button class="btn btn-primary" disabled={!changing.protected && !changeNew}>{t('comfort.pgp.savePassphrase')}</button>
      <button type="button" class="btn btn-ghost" onclick={() => { changing = null; changeCurrent = changeNew = ''; }}>{t('common.action.cancel')}</button>
    </form>
  {/if}
  {#if unlocking}
    <PgpUnlockDialog id={unlocking.id} email={unlocking.email} onclose={() => (unlocking = null)} ondone={() => { unlocking = null; void load(); }} />
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}

  <ul class="keys">
    {#each keys as k (k.id)}
      <li class:expired={k.status !== 'valid'}>
        <div class="main">
          <span class="email">{k.email || k.userId}</span>
          <span class="badge">{t(`comfort.pgp.source.${k.source}` as MessageKey)}</span>
          <span class="badge" class:ok={k.trust === 'verified'} class:bad={k.trust === 'revoked' || k.trust === 'expired'}>
            {#if k.trust === 'verified'}<ShieldCheck size={11} />{/if}
            {t(`comfort.pgp.trust.${k.trust}` as MessageKey)}
          </span>
        </div>
        {#if k.own}<div class="sub">{k.protected ? (k.locked ? t('comfort.pgp.stateLocked') : t('comfort.pgp.stateUnlocked', { time: fmt().time(k.unlockedUntil ?? '') })) : t('comfort.pgp.stateNoPassphrase')}</div>{/if}
        <div class="sub mono">{fingerprint(k.fingerprint)}</div>
        <div class="sub">{k.algorithm} · {fmt().date(k.created)}{#if k.expires} · {t('comfort.smime.validUntil', { date: fmt().date(k.expires) })}{/if}</div>
        <div class="acts">
          {#if k.own && k.locked}<button class="btn btn-ghost sm" onclick={() => (unlocking = k)}>{t('comfort.pgp.unlock')}</button>{/if}
          {#if k.own}<button class="btn btn-ghost sm" onclick={() => { changing = k; changeCurrent = changeNew = ''; }}>{t('comfort.pgp.changePassphrase')}</button>{/if}
          {#if !k.own}<button class="btn btn-ghost sm" onclick={() => trust(k)}>{k.trust === 'verified' ? t('comfort.pgp.unverify') : t('comfort.pgp.verify')}</button>{/if}
          <button class="icon-btn" aria-label={t('comfort.pgp.exportPublic')} title={t('comfort.pgp.exportPublic')} onclick={() => exportPublic(k)}><Download size={15} /></button>
          {#if k.own}<button class="icon-btn" aria-label={t('comfort.pgp.exportSecret')} title={t('comfort.pgp.exportSecret')} onclick={() => (exporting = k)}><KeyRound size={15} /></button>{/if}
          <button class="icon-btn" aria-label={t('comfort.pgp.delete')} title={t('comfort.pgp.delete')} onclick={() => del(k)}><Trash size={15} /></button>
        </div>
      </li>
    {:else}
      <li class="empty">{t('comfort.pgp.none')}</li>
    {/each}
  </ul>

  <h3>{t('comfort.pgp.peersTitle')}</h3>
  <p class="hint">{t('comfort.pgp.peersIntro')}</p>
  <ul class="keys">
    {#each peers as p (p.address)}
      <li>
        <div class="main">
          <span class="email">{p.address}</span>
          <span class="badge">{t(p.gossip ? 'comfort.pgp.source.gossip' : 'comfort.pgp.source.autocrypt')}</span>
          <span class="badge" class:ok={p.trust === 'verified'}>{t(`comfort.pgp.trust.${p.trust}` as MessageKey)}</span>
          {#if p.keyChanged}<span class="badge bad">{t('comfort.pgp.keyChanged')}</span>{/if}
        </div>
        <div class="sub mono">{fingerprint(p.fingerprint)}</div>
        <div class="acts">
          {#if p.trust !== 'verified'}<button class="btn btn-ghost sm" onclick={() => verifyPeer(p)}>{t('comfort.pgp.verify')}</button>{/if}
          <button class="icon-btn" aria-label={t('comfort.pgp.delete')} title={t('comfort.pgp.delete')} onclick={() => delPeer(p)}><Trash size={15} /></button>
        </div>
      </li>
    {:else}
      <li class="empty">{t('comfort.pgp.nonePeers')}</li>
    {/each}
  </ul>

  <h3>{t('comfort.pgp.lookupTitle')}</h3>
  <p class="hint">{t('comfort.pgp.lookupIntro')}</p>
  <div class="row">
    <input class="input" type="email" bind:value={lookupEmail} placeholder={t('comfort.pgp.email')} aria-label={t('comfort.pgp.email')} />
    <button class="btn" disabled={busy || !lookupEmail} onclick={() => lookup('wkd')}>{t('comfort.pgp.lookupWkd')}</button>
    <button class="btn" disabled={busy || !lookupEmail} onclick={() => lookup('keyserver')}>{t('comfort.pgp.lookupKeyserver')}</button>
  </div>

  {#if s}
    <div class="defaults">
      <label class="chk"><input type="checkbox" bind:checked={s.pgp.signByDefault} /> {t('comfort.pgp.signByDefault')}</label>
      <label class="chk"><input type="checkbox" bind:checked={s.pgp.sendAutocrypt} /> {t('comfort.pgp.sendAutocrypt')}</label>
      <label class="chk"><input type="checkbox" bind:checked={s.pgp.preferEncrypt} /> {t('comfort.pgp.preferEncrypt')}</label>
      <button class="btn" onclick={saveDefaults}>{t('common.action.save')}</button>
    </div>
  {/if}
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
  }
  .head-acts {
    display: flex;
    gap: 6px;
  }
  h2 {
    margin: 0;
    font-size: 15px;
  }
  h3 {
    margin: 16px 0 0;
    font-size: 13.5px;
  }
  .hint {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .row {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    font-size: 13px;
    margin-bottom: 8px;
  }
  .row .input {
    flex: 1;
    min-width: 160px;
  }
  .error {
    color: #dc2626;
    font-size: 13px;
  }
  .keys {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .keys li {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 2px 10px;
    padding: 8px 0;
    border-bottom: 1px solid var(--tm-line);
  }
  .main {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
  }
  .email {
    font-weight: 600;
    font-size: 13.5px;
  }
  .sub {
    grid-column: 1;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .mono {
    font-family: var(--tm-mono, monospace);
    overflow-wrap: anywhere;
  }
  .acts {
    grid-row: 1 / span 4;
    grid-column: 2;
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .badge.ok {
    background: color-mix(in srgb, #16a34a 16%, transparent);
    color: #15803d;
  }
  .badge.bad {
    background: color-mix(in srgb, #dc2626 16%, transparent);
    color: #b91c1c;
  }
  .expired .email {
    color: var(--tm-muted);
  }
  .empty {
    color: var(--tm-muted);
    font-size: 13px;
  }
  .defaults {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 12px;
    align-items: flex-start;
  }
  .chk {
    display: flex;
    gap: 6px;
    align-items: center;
    font-size: 13px;
  }
  .sm {
    height: 28px;
    padding: 0 9px;
    font-size: 12.5px;
    width: auto;
  }
</style>
