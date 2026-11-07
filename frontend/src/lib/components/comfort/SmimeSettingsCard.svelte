<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> S/MIME certificate manager: import PKCS#12 / certificates, trust, delete, defaults. -->
<script lang="ts">
  import { isNativeShell } from '../../platform';
  import BrowserOnlyHint from '../common/BrowserOnlyHint.svelte';
  import Upload from '@lucide/svelte/icons/upload';
  import Trash from '@lucide/svelte/icons/trash';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import { app, comfort } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import type { ComfortSettings, SmimeCertificate } from '../../api/comfort';

  let certs = $state<SmimeCertificate[]>([]);
  let fileInput = $state<HTMLInputElement>();
  let pending = $state<File | null>(null);
  let password = $state('');
  let error = $state<string | null>(null);
  let s = $state<ComfortSettings | null>(null);

  async function load() {
    certs = await comfort.api.listCertificates().catch(() => []);
  }
  $effect(() => {
    void load();
    void comfort.loadSettings().then((v) => v && (s = structuredClone($state.snapshot(v) as ComfortSettings)));
  });

  function pick(f: File | undefined) {
    if (!f) return;
    error = null;
    if (/\.(p12|pfx)$/i.test(f.name)) pending = f;
    else void doImport(f, '');
  }

  async function doImport(f: File, pw: string) {
    try {
      const c = await comfort.api.importCertificate(f, pw || undefined);
      app.toast(t('comfort.smime.imported', { email: c.email }));
      pending = null;
      password = '';
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function trust(c: SmimeCertificate) {
    await comfort.api.trustCertificate(c.id, !c.trusted);
    await load();
  }

  async function del(c: SmimeCertificate) {
    if (!confirm(t('comfort.smime.confirmDelete', { email: c.email }))) return;
    await comfort.api.deleteCertificate(c.id);
    await load();
  }

  async function saveDefaults() {
    if (s) await comfort.saveSettings($state.snapshot(s) as ComfortSettings);
    app.toast(t('notifications.saved'));
  }

  const native = isNativeShell();
</script>

<section class="card" id="set-smime" aria-labelledby="set-smime-h">
  <div class="head">
    <h2 id="set-smime-h">{t('comfort.smime.title')}</h2>
    <button class="btn" onclick={() => fileInput?.click()}><Upload size={15} /> {t('comfort.smime.import')}</button>
    <input type="file" accept=".p12,.pfx,.cer,.crt,.pem,.der" hidden bind:this={fileInput} onchange={(e) => pick((e.currentTarget as HTMLInputElement).files?.[0])} />
  </div>
  <p class="hint">{t('comfort.smime.intro')}</p>
  {#if pending}
    <div class="pw">
      <KeyRound size={15} aria-hidden="true" />
      <span>{pending.name}</span>
      <input class="input" type="password" bind:value={password} placeholder={t('comfort.smime.password')} aria-label={t('comfort.smime.password')} />
      <button class="btn btn-primary" onclick={() => pending && doImport(pending, password)}>{t('comfort.smime.importNow')}</button>
    </div>
  {/if}
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if native && certs.some((c) => c.own)}<BrowserOnlyHint what={t('errors.privateKeyTitle')} />{/if}
  <ul class="certs">
    {#each certs as c (c.id)}
      <li class:expired={c.expired}>
        <div class="main">
          <span class="email">{c.email}</span>
          <span class="badge">{t(`comfort.smime.source.${c.source}`)}</span>
          {#if c.trusted}<span class="badge ok"><ShieldCheck size={11} /> {t('comfort.smime.trusted')}</span>{/if}
          {#if c.expired}<span class="badge bad">{t('comfort.smime.expired')}</span>{/if}
        </div>
        <div class="sub">{c.subject} · {t('comfort.smime.issuer', { issuer: c.issuer })} · {t('comfort.smime.validUntil', { date: fmt().date(c.notAfter) })}</div>
        <div class="acts">
          {#if !c.own}<button class="btn btn-ghost sm" onclick={() => trust(c)}>{c.trusted ? t('comfort.smime.untrust') : t('comfort.smime.trust')}</button>{/if}
          {#if !(native && c.own)}<button class="icon-btn" aria-label={t('comfort.smime.delete')} title={t('comfort.smime.delete')} onclick={() => del(c)}><Trash size={15} /></button>{/if}
        </div>
      </li>
    {:else}
      <li class="empty">{t('comfort.smime.none')}</li>
    {/each}
  </ul>
  {#if s}
    <div class="defaults">
      <label class="chk"><input type="checkbox" bind:checked={s.smime.signByDefault} /> {t('comfort.smime.signByDefault')}</label>
      <label class="chk"><input type="checkbox" bind:checked={s.smime.encryptAutomatically} /> {t('comfort.smime.encryptAuto')}</label>
      <label class="chk">
        {t('comfort.smime.prefer')}
        <select class="input sm" bind:value={s.smime.prefer}>
          <option value="pgp">{t('comfort.smime.preferPgp')}</option>
          <option value="smime">{t('comfort.smime.preferSmime')}</option>
        </select>
      </label>
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
  h2 {
    margin: 0;
    font-size: 15px;
  }
  .hint {
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .pw {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    font-size: 13px;
    margin-bottom: 8px;
  }
  .pw .input {
    flex: 1;
    min-width: 160px;
  }
  .error {
    color: #dc2626;
    font-size: 13px;
  }
  .certs {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .certs li {
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
  .acts {
    grid-row: 1 / span 2;
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
