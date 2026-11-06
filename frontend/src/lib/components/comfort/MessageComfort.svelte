<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Reading pane banners and cards of the comfort features: spam warning,
  S/MIME status, newsletter bar (unsubscribe / archive all / archive
  automatically), e-invoice card with "pay by" task, smart category.
-->
<script lang="ts">
  import ShieldAlert from '@lucide/svelte/icons/shield-alert';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import ShieldQuestion from '@lucide/svelte/icons/shield-question-mark';
  import Lock from '@lucide/svelte/icons/lock';
  import Newspaper from '@lucide/svelte/icons/newspaper';
  import Receipt from '@lucide/svelte/icons/receipt';
  import ListTodo from '@lucide/svelte/icons/list-todo';
  import Copy from '@lucide/svelte/icons/copy';
  import { app, comfort } from '../../state';
  import { fmt, t } from '../../i18n/index.svelte';
  import { CATEGORIES, type CategoryId, type MessageInsights } from '../../api/comfort';
  import { openExternal } from '../../platform';

  interface Props {
    messageId: number;
  }
  let { messageId }: Props = $props();

  let ins = $state<MessageInsights | null>(null);
  let busy = $state(false);
  let taskCreated = $state(false);

  $effect(() => {
    const id = messageId;
    ins = comfort.insights[id] ?? null;
    taskCreated = false;
    void comfort.loadSettings();
    void comfort.loadInsights(id).then((v) => {
      if (id === messageId) ins = v;
    });
  });

  async function reload() {
    ins = await comfort.loadInsights(messageId, true);
  }

  async function markSpam(spam: boolean) {
    busy = true;
    try {
      await comfort.api.markSpam([messageId], spam, true);
      app.toast(spam ? t('comfort.spam.markedSpam') : t('comfort.spam.markedHam'));
      await reload();
    } catch (e) {
      app.toast(String(e instanceof Error ? e.message : e), undefined, 'error');
    } finally {
      busy = false;
    }
  }

  async function unsubscribe() {
    if (!ins?.unsubscribe) return;
    busy = true;
    try {
      const r = await comfort.api.unsubscribeMessage(messageId);
      if (r.done) app.toast(r.method === 'mailto' ? t('comfort.newsletter.unsubscribedMail') : t('comfort.newsletter.unsubscribed'));
      else if (r.openUrl) {
        openExternal(r.openUrl);
        app.toast(t('comfort.newsletter.openedLink'));
      } else app.toast(t('comfort.newsletter.failed', { error: r.error ?? '' }), undefined, 'error');
      await reload();
    } finally {
      busy = false;
    }
  }

  async function archiveAll() {
    if (!ins?.unsubscribe) return;
    const key = ins.unsubscribe.listId || ins.sender || '';
    const n = await comfort.api.archiveSubscription(key);
    app.toast(t('comfort.newsletter.archived', { count: n }));
  }

  async function autoArchive() {
    if (!ins?.unsubscribe) return;
    await comfort.api.autoArchiveSubscription(ins.unsubscribe.listId || ins.sender || '');
    app.toast(t('comfort.newsletter.autoArchiveCreated'));
  }

  async function invoiceTask() {
    await comfort.api.createInvoiceTask(messageId);
    taskCreated = true;
    app.toast(t('comfort.invoice.taskCreated'));
  }

  function setCategory(e: Event) {
    const c = (e.currentTarget as HTMLSelectElement).value as CategoryId;
    void comfort.changeCategory([messageId], c).then(reload);
  }

  function amount(a?: string, cur?: string): string {
    if (!a) return '';
    const n = Number(a);
    if (Number.isNaN(n)) return `${a} ${cur ?? ''}`;
    try {
      return new Intl.NumberFormat(undefined, { style: 'currency', currency: cur || 'EUR' }).format(n);
    } catch {
      return `${a} ${cur ?? ''}`;
    }
  }

  const formatDate = (iso: string) => fmt().date(iso);
  const sig = $derived(ins?.smime?.signature);
  const pgpSig = $derived(ins?.pgp?.signature);
  const pgpBad = $derived(pgpSig === 'invalid' || pgpSig === 'mismatch' || pgpSig === 'revoked_key' || (ins?.pgp?.encrypted && !ins.pgp.decrypted));
</script>

{#if ins}
  {#if ins.spam.level === 'spam' || ins.spam.level === 'unsure'}
    <div class="banner spam" class:unsure={ins.spam.level === 'unsure'} role="alert">
      <ShieldAlert size={16} aria-hidden="true" />
      <span class="txt">
        {ins.spam.level === 'spam' ? t('comfort.spam.isSpam', { score: ins.spam.score }) : t('comfort.spam.suspicious', { score: ins.spam.score })}
        {#if ins.spam.reasons?.length}<span class="muted"> · {ins.spam.reasons.slice(0, 2).join(', ')}</span>{/if}
      </span>
      <button class="btn" disabled={busy} onclick={() => markSpam(false)}>{t('comfort.spam.notSpam')}</button>
      {#if ins.spam.level === 'unsure'}<button class="btn" disabled={busy} onclick={() => markSpam(true)}>{t('comfort.spam.spam')}</button>{/if}
    </div>
  {/if}

  {#if ins.smime?.present}
    <div class="banner smime" class:ok={sig === 'trusted'} class:bad={sig === 'invalid' || sig === 'mismatch'} role="status">
      {#if sig === 'trusted'}<ShieldCheck size={16} aria-hidden="true" />{:else if ins.smime.encrypted && !ins.smime.signed}<Lock size={16} aria-hidden="true" />{:else}<ShieldQuestion size={16} aria-hidden="true" />{/if}
      <span class="txt">
        {#if ins.smime.encrypted}{ins.smime.decrypted ? t('comfort.smime.decrypted') : t('comfort.smime.cannotDecrypt')}{/if}
        {#if ins.smime.signed}
          {t(`comfort.smime.sig.${sig ?? 'invalid'}`, { name: ins.smime.signerName || ins.smime.signerEmail || '' })}
          {#if ins.smime.issuer}<span class="muted"> · {t('comfort.smime.issuer', { issuer: ins.smime.issuer })}</span>{/if}
        {/if}
      </span>
    </div>
  {/if}

  {#if ins.pgp?.present}
    <div class="banner pgp" class:ok={pgpSig === 'valid' && !pgpBad} class:bad={pgpBad} role="status">
      {#if pgpBad}<ShieldAlert size={16} aria-hidden="true" />{:else if pgpSig === 'valid'}<ShieldCheck size={16} aria-hidden="true" />{:else if ins.pgp.encrypted}<Lock size={16} aria-hidden="true" />{:else}<ShieldQuestion size={16} aria-hidden="true" />{/if}
      <span class="txt">
        {#if ins.pgp.encrypted}{ins.pgp.decrypted ? t('comfort.pgp.status.decrypted') : t('comfort.pgp.status.cannotDecrypt')}{/if}
        {#if ins.pgp.signed && pgpSig}
          {t(`comfort.pgp.status.sig.${pgpSig}`)}
          {#if pgpSig === 'valid' && ins.pgp.signerTrust}<span class="muted"> · {t(`comfort.pgp.trust.${ins.pgp.signerTrust}`)}</span>{/if}
        {:else if ins.pgp.decrypted}{t('comfort.pgp.status.unsigned')}{/if}
        {#if ins.pgp.signerFingerprint && pgpSig !== 'valid'}<span class="muted"> · {ins.pgp.signerFingerprint}</span>{/if}
        {#if ins.pgp.protectedSubject}<span class="muted"> · {t('comfort.pgp.status.subject', { subject: ins.pgp.protectedSubject })}</span>{/if}
        {#if ins.pgp.partiallyProtected}<span class="muted"> · {t('comfort.pgp.status.partial')}</span>{/if}
      </span>
    </div>
  {/if}

  {#if ins.unsubscribe && (comfort.settings?.newsletter.showBar ?? true)}
    <div class="banner news" role="note">
      <Newspaper size={16} aria-hidden="true" />
      <span class="txt">{ins.unsubscribed ? t('comfort.newsletter.alreadyUnsubscribed', { name: ins.senderName || ins.sender || '' }) : t('comfort.newsletter.bar', { name: ins.senderName || ins.sender || '' })}</span>
      {#if !ins.unsubscribed}<button class="btn" disabled={busy} onclick={unsubscribe}>{t('comfort.newsletter.unsubscribe')}</button>{/if}
      <button class="btn btn-ghost" onclick={archiveAll}>{t('comfort.newsletter.archiveAll')}</button>
      <button class="btn btn-ghost" onclick={autoArchive}>{t('comfort.newsletter.autoArchive')}</button>
    </div>
  {/if}

  {#if ins.invoice}
    {@const inv = ins.invoice}
    <section class="invoice" aria-label={t('comfort.invoice.title')}>
      <div class="inv-head">
        <Receipt size={16} aria-hidden="true" />
        <strong>{t('comfort.invoice.title')}</strong>
        <span class="fmt">{t(`comfort.invoice.format.${inv.format}`)}</span>
      </div>
      <dl>
        {#if inv.seller}<dt>{t('comfort.invoice.seller')}</dt><dd>{inv.seller}</dd>{/if}
        {#if inv.number}<dt>{t('comfort.invoice.number')}</dt><dd>{inv.number}</dd>{/if}
        {#if inv.amount}<dt>{t('comfort.invoice.amount')}</dt><dd class="amount">{amount(inv.amount, inv.currency)}</dd>{/if}
        {#if inv.issued}<dt>{t('comfort.invoice.issued')}</dt><dd>{formatDate(inv.issued)}</dd>{/if}
        {#if inv.due}<dt>{t('comfort.invoice.due')}</dt><dd>{formatDate(inv.due)}</dd>{/if}
        {#if inv.iban}
          <dt>{t('comfort.invoice.iban')}</dt>
          <dd>
            <code>{inv.iban}</code>
            <button class="icon-btn sm" title={t('comfort.invoice.copyIban')} aria-label={t('comfort.invoice.copyIban')} onclick={() => navigator.clipboard?.writeText(inv.iban ?? '')}><Copy size={13} /></button>
          </dd>
        {/if}
        {#if inv.reference}<dt>{t('comfort.invoice.reference')}</dt><dd>{inv.reference}</dd>{/if}
      </dl>
      <button class="btn" disabled={taskCreated} onclick={invoiceTask}><ListTodo size={15} /> {inv.due ? t('comfort.invoice.payBy', { date: formatDate(inv.due) }) : t('comfort.invoice.payTask')}</button>
    </section>
  {/if}

  {#if ins.category && (comfort.settings?.categories.enabled ?? true)}
    <div class="cat">
      <label for="cat-{messageId}">{t('comfort.category.label')}</label>
      <select id="cat-{messageId}" class="input sm" value={ins.category} onchange={setCategory}>
        {#each CATEGORIES as c (c)}<option value={c}>{t(`comfort.category.${c}`)}</option>{/each}
      </select>
      {#if ins.categorySource === 'user'}<span class="muted">{t('comfort.category.manual')}</span>{/if}
    </div>
  {/if}
{/if}

<style>
  .banner {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0 24px 10px;
    padding: 8px 8px 8px 12px;
    border-radius: 10px;
    font-size: 13px;
    background: color-mix(in srgb, var(--tm-accent) 8%, var(--tm-surface));
    flex-wrap: wrap;
  }
  .banner .txt {
    flex: 1;
    min-width: 180px;
  }
  .banner .btn {
    height: 28px;
  }
  .spam {
    background: color-mix(in srgb, #dc2626 13%, var(--tm-surface));
  }
  .spam.unsure {
    background: color-mix(in srgb, var(--tm-warn) 14%, var(--tm-surface));
  }
  .pgp.ok {
    background: color-mix(in srgb, #16a34a 12%, var(--tm-surface));
  }
  .pgp.bad {
    background: color-mix(in srgb, #dc2626 13%, var(--tm-surface));
  }
  .smime.ok {
    background: color-mix(in srgb, #16a34a 12%, var(--tm-surface));
  }
  .smime.bad {
    background: color-mix(in srgb, #dc2626 13%, var(--tm-surface));
  }
  .muted {
    color: var(--tm-muted);
  }
  .invoice {
    margin: 0 24px 12px;
    padding: 12px 14px;
    border: 1px solid var(--tm-line);
    border-radius: 12px;
    background: var(--tm-surface);
    max-width: 520px;
  }
  .inv-head {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 8px;
  }
  .fmt {
    margin-left: auto;
    font-size: 11px;
    padding: 1px 8px;
    border-radius: 8px;
    background: color-mix(in srgb, var(--tm-accent) 14%, transparent);
    color: var(--tm-accent-text);
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 3px 14px;
    margin: 0 0 10px;
    font-size: 13px;
  }
  dt {
    color: var(--tm-muted);
  }
  dd {
    margin: 0;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .amount {
    font-weight: 650;
  }
  .cat {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0 24px 10px;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .cat .input {
    width: auto;
    height: 28px;
    padding: 0 8px;
    font-size: 12.5px;
  }
</style>
