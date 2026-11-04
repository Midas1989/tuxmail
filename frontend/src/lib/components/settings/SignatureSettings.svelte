<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Signatures (KONZEPT 8.2) and the signatures used per identity. -->
<script lang="ts">
  import Plus from '@lucide/svelte/icons/plus';
  import Trash from '@lucide/svelte/icons/trash';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { Identity, Signature } from '../../types';

  async function reload() {
    app.signatures = await app.api.listSignatures();
  }
  async function save(sig: Signature) {
    // The HTML version is regenerated from the plain text.
    await app.api.saveSignature({ ...sig, html: '' });
    await reload();
  }
  async function add() {
    const name = app.identities[0]?.name ?? '';
    const sig = await app.api.saveSignature({ id: 0, name: t('settings.signatures.newName'), html: '', plain: name, placement: 'below_reply' });
    await reload();
    // A first signature becomes the default of identities without one.
    for (const i of app.identities) if (!i.defaultSignatureId) await app.updateIdentity({ ...i, defaultSignatureId: sig.id });
  }
  async function remove(id: number) {
    await app.api.deleteSignature(id);
    await reload();
    app.identities = await app.api.listIdentities();
  }
  function setIdentity(i: Identity, field: 'defaultSignatureId' | 'replySignatureId', value: string) {
    void app.updateIdentity({ ...i, [field]: value ? Number(value) : 0 });
  }
</script>

<section class="card" aria-labelledby="set-signatures">
  <div class="head">
    <h2 id="set-signatures">{t('settings.signatures.title')}</h2>
    <button class="btn" onclick={add}><Plus size={14} /> {t('settings.signatures.add')}</button>
  </div>
  <p class="hint">{t('settings.signatures.hint')}</p>
  <ul>
    {#each app.signatures as sig (sig.id)}
      <li data-signature-id={sig.id}>
        <div class="row">
          <input class="input" value={sig.name} aria-label={t('settings.signatures.name')} onchange={(e) => save({ ...sig, name: (e.currentTarget as HTMLInputElement).value })} />
          <button class="icon-btn" aria-label={t('settings.signatures.delete', { name: sig.name })} onclick={() => remove(sig.id)}><Trash size={15} /></button>
        </div>
        <textarea class="input text" rows="3" value={sig.plain} aria-label={t('settings.signatures.text')} onchange={(e) => save({ ...sig, plain: (e.currentTarget as HTMLTextAreaElement).value })}></textarea>
      </li>
    {/each}
  </ul>
  {#if app.signatures.length && app.identities.length}
    <table class="ids">
      <thead>
        <tr><th>{t('settings.signatures.identity')}</th><th>{t('settings.signatures.default')}</th><th>{t('settings.signatures.reply')}</th></tr>
      </thead>
      <tbody>
        {#each app.identities as i (i.id)}
          <tr>
            <td class="email">{i.email}</td>
            <td>
              <select class="input" value={String(i.defaultSignatureId ?? '')} aria-label={t('settings.signatures.defaultFor', { email: i.email })} onchange={(e) => setIdentity(i, 'defaultSignatureId', (e.currentTarget as HTMLSelectElement).value)}>
                <option value="">{t('settings.signatures.none')}</option>
                {#each app.signatures as s (s.id)}<option value={String(s.id)}>{s.name}</option>{/each}
              </select>
            </td>
            <td>
              <select class="input" value={String(i.replySignatureId ?? '')} aria-label={t('settings.signatures.replyFor', { email: i.email })} onchange={(e) => setIdentity(i, 'replySignatureId', (e.currentTarget as HTMLSelectElement).value)}>
                <option value="">{t('settings.signatures.sameAsDefault')}</option>
                {#each app.signatures as s (s.id)}<option value={String(s.id)}>{s.name}</option>{/each}
              </select>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
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
    justify-content: space-between;
    align-items: center;
  }
  h2 {
    margin: 0;
    font-size: 15px;
    font-weight: 650;
  }
  .hint {
    margin: 4px 0 10px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .text {
    margin-top: 4px;
    height: auto;
    padding: 6px 10px;
    resize: vertical;
    font-family: var(--tm-mono);
    font-size: 12.5px;
  }
  .ids {
    width: 100%;
    margin-top: 12px;
    border-collapse: collapse;
    font-size: 12.5px;
  }
  .ids th {
    text-align: left;
    font-weight: 600;
    color: var(--tm-muted);
    padding: 4px 4px 4px 0;
  }
  .ids td {
    padding: 3px 4px 3px 0;
  }
  .email {
    overflow-wrap: anywhere;
  }
</style>
