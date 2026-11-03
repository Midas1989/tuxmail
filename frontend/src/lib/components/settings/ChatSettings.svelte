<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings -> Chats: Enter behaviour and signatures for mails written in chat mode (per identity). -->
<script lang="ts">
  import { app, prefs } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import type { Identity } from '../../types';

  function setMode(i: Identity, mode: 'full' | 'short' | 'off') {
    void app.updateIdentity({ ...i, chatSignatureMode: mode });
  }
  function setShort(i: Identity, id: string) {
    void app.updateIdentity({ ...i, chatSignatureId: id ? Number(id) : undefined, chatSignatureMode: id ? (i.chatSignatureMode ?? 'full') : i.chatSignatureMode === 'short' ? 'full' : i.chatSignatureMode });
  }
</script>

<section class="card" aria-labelledby="set-chat">
  <h2 id="set-chat">{t('settings.chat.title')}</h2>
  <label class="check">
    <input
      type="checkbox"
      checked={prefs.chatEnterSends}
      onchange={(e) => {
        prefs.chatEnterSends = (e.currentTarget as HTMLInputElement).checked;
        prefs.save(false);
      }}
    />
    <span>{t('settings.chat.enterSends')}<small>{t('settings.chat.enterSendsHint')}</small></span>
  </label>

  <h3>{t('settings.chat.signatures')}</h3>
  <p class="hint">{t('settings.chat.signaturesHint')}</p>
  <div class="table" role="table" aria-label={t('settings.chat.signatures')}>
    {#each app.identities as i (i.id)}
      <div class="row" role="row">
        <span class="ident" role="cell">{i.email}</span>
        <span role="cell">
          <select class="input" aria-label={t('settings.chat.modeFor', { email: i.email })} value={i.chatSignatureMode ?? 'full'} onchange={(e) => setMode(i, (e.currentTarget as HTMLSelectElement).value as 'full' | 'short' | 'off')}>
            <option value="full">{t('settings.chat.mode.full')}</option>
            <option value="short">{t('settings.chat.mode.short')}</option>
            <option value="off">{t('settings.chat.mode.off')}</option>
          </select>
        </span>
        <span role="cell">
          <select class="input" aria-label={t('settings.chat.shortFor', { email: i.email })} value={i.chatSignatureId ?? ''} onchange={(e) => setShort(i, (e.currentTarget as HTMLSelectElement).value)}>
            <option value="">{t('settings.chat.noShort')}</option>
            {#each app.signatures as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
          </select>
        </span>
      </div>
    {/each}
  </div>
</section>

<style>
  .card {
    padding: 18px 20px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius-lg);
  }
  h2 {
    margin: 0 0 12px;
    font-size: 15px;
    font-weight: 650;
  }
  h3 {
    margin: 16px 0 2px;
    font-size: 13px;
    font-weight: 650;
  }
  .hint {
    margin: 0 0 8px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .check {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    font-size: 13.5px;
  }
  .check input {
    margin-top: 3px;
    accent-color: var(--tm-accent);
  }
  .check span {
    display: flex;
    flex-direction: column;
  }
  small {
    color: var(--tm-muted);
    font-size: 12px;
  }
  .table {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .row {
    display: grid;
    grid-template-columns: 1.3fr 1fr 1fr;
    gap: 8px;
    align-items: center;
  }
  .ident {
    font-size: 13px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .row .input {
    height: 30px;
    font-size: 12.5px;
  }
  @media (max-width: 639px) {
    .row {
      grid-template-columns: 1fr;
    }
  }
</style>
