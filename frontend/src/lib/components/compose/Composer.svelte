<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts" module>
  import type { AttachmentDelivery } from '../../types';

  /** Files above this size get a cloud link suggested (KONZEPT 12.1 #4). */
  export const LARGE_ATTACHMENT = 10 * 1024 * 1024;

  export interface DraftAttachment {
    key: string;
    filename: string;
    mimeType: string;
    size: number;
    delivery: AttachmentDelivery;
    file?: File;
    /** Forwarded attachments already stored in the core. */
    existingId?: number;
  }

  const REMEMBER_KEY = 'tuxmail.pwdelivery.v1';
  export function rememberedDelivery(email: string): string | undefined {
    try {
      return (JSON.parse(localStorage.getItem(REMEMBER_KEY) ?? '{}') as Record<string, string>)[email.toLowerCase()];
    } catch {
      return undefined;
    }
  }
  export function rememberDelivery(email: string, mode: string): void {
    try {
      const map = JSON.parse(localStorage.getItem(REMEMBER_KEY) ?? '{}') as Record<string, string>;
      map[email.toLowerCase()] = mode;
      localStorage.setItem(REMEMBER_KEY, JSON.stringify(map));
    } catch {
      /* ignore */
    }
  }
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import Bold from '@lucide/svelte/icons/letter-b';
  import Italic from '@lucide/svelte/icons/italic';
  import Underline from '@lucide/svelte/icons/underline';
  import List from '@lucide/svelte/icons/list';
  import ListOrdered from '@lucide/svelte/icons/list-ordered';
  import Quote from '@lucide/svelte/icons/quote';
  import LinkIcon from '@lucide/svelte/icons/link';
  import Paperclip from '@lucide/svelte/icons/paperclip';
  import Trash from '@lucide/svelte/icons/trash';
  import SendIcon from '@lucide/svelte/icons/send';
  import Signature from '@lucide/svelte/icons/signature';
  import Cloud from '@lucide/svelte/icons/cloud';
  import { app } from '../../state';
  import type { ComposerInit } from '../../state';
  import type { Address, OutgoingAttachment, PasswordDelivery, PasswordDeliveryMode } from '../../types';
  import { formatSize, t } from '../../i18n/index.svelte';
  import Modal from '../common/Modal.svelte';
  import RecipientInput, { isValidEmail } from './RecipientInput.svelte';
  import PasswordDeliveryDialog, { type MessengerTarget } from './PasswordDeliveryDialog.svelte';

  interface Props {
    init: ComposerInit;
    onclose: () => void;
  }
  let { init, onclose }: Props = $props();

  // Snapshot of the initial values: the composer owns its draft afterwards.
  const start = untrack(() => $state.snapshot(init)) as ComposerInit;
  let identityId = $state<number>(start.identityId ?? app.defaultIdentity()?.id ?? 0);
  let to = $state<Address[]>(start.to ?? []);
  let cc = $state<Address[]>(start.cc ?? []);
  let bcc = $state<Address[]>([]);
  let showCc = $state((start.cc?.length ?? 0) > 0);
  let showBcc = $state(false);
  let subject = $state(start.subject ?? '');
  let attachments = $state<DraftAttachment[]>(
    (start.forwardAttachments ?? []).map((a) => ({ key: 'fwd' + a.id, filename: a.filename, mimeType: a.mimeType, size: a.size, delivery: 'attach' as const, existingId: a.id })),
  );
  let editor = $state<HTMLDivElement>();
  let fileInput = $state<HTMLInputElement>();
  let sending = $state(false);
  let error = $state<string | null>(null);
  let askPassword = $state(false);
  let dragOver = $state(false);

  const identity = $derived(app.identities.find((i) => i.id === identityId));
  const isReply = start.mode === 'reply' || start.mode === 'replyAll';
  const title = $derived(
    start.mode === 'reply' || start.mode === 'replyAll' ? t('compose.titleReply') : start.mode === 'forward' ? t('compose.titleForward') : t('compose.titleNew'),
  );

  function signatureHtml(idId: number): string {
    const sig = app.signatureFor(idId, isReply);
    return sig ? `-- <br>${sig.html}` : '';
  }

  // Initial editor content: text, signature block, quote.
  $effect(() => {
    if (!editor || editor.dataset.ready) return;
    editor.dataset.ready = '1';
    const sig = signatureHtml(identityId);
    editor.innerHTML =
      (start.bodyHtml || '<p><br></p>') + (sig ? `<div data-signature="1" class="sig">${sig}</div>` : '') + (start.quoteHtml ? `<div data-quote="1">${start.quoteHtml}</div>` : '');
  });

  // Signature follows the selected identity.
  function onIdentityChange() {
    if (!editor) return;
    const html = signatureHtml(identityId);
    let block = editor.querySelector<HTMLElement>('[data-signature]');
    if (!html) {
      block?.remove();
      return;
    }
    if (!block) {
      block = document.createElement('div');
      block.dataset.signature = '1';
      block.className = 'sig';
      const quote = editor.querySelector('[data-quote]');
      editor.insertBefore(block, quote);
    }
    block.innerHTML = html;
  }

  function insertSignature() {
    onIdentityChange();
  }

  function exec(cmd: string, value?: string) {
    editor?.focus();
    document.execCommand(cmd, false, value);
  }

  function addLink() {
    const url = prompt(t('compose.linkPrompt'), 'https://');
    if (url && /^(https?:|mailto:)/i.test(url)) exec('createLink', url);
  }

  function addFiles(files: FileList | File[] | null) {
    if (!files) return;
    for (const f of files) {
      attachments.push({
        key: `${f.name}-${f.size}-${Math.random().toString(36).slice(2, 6)}`,
        filename: f.name,
        mimeType: f.type || 'application/octet-stream',
        size: f.size,
        delivery: f.size > LARGE_ATTACHMENT ? 'nextcloud' : 'attach',
        file: f,
      });
    }
  }

  const cloudAttachments = $derived(attachments.filter((a) => a.delivery !== 'attach'));
  const totalAttachSize = $derived(attachments.filter((a) => a.delivery === 'attach').reduce((n, a) => n + a.size, 0));

  /** Messenger accounts on which the first recipient is reachable. */
  const messengerTargets = $derived.by((): MessengerTarget[] => {
    const first = to[0] ?? cc[0];
    if (!first) return [];
    const p = app.personByEmail(first.email);
    if (!p) return [];
    const out: MessengerTarget[] = [];
    for (const acc of app.messengerAccounts) {
      const ch = acc.kind === 'signal' ? 'signal' : 'whatsapp';
      if (p.handles.some((h) => h.kind === ch)) out.push({ accountId: acc.id, channel: ch, label: acc.displayName });
    }
    return out;
  });

  function validate(): boolean {
    const all = [...to, ...cc, ...bcc];
    if (all.length === 0) {
      error = t('compose.errNoRecipient');
      return false;
    }
    const bad = all.find((a) => !isValidEmail(a.email));
    if (bad) {
      error = t('compose.errInvalid', { email: bad.email });
      return false;
    }
    error = null;
    return true;
  }

  function trySend() {
    if (sending || !validate()) return;
    if (cloudAttachments.length) {
      const first = (to[0] ?? cc[0])?.email;
      const remembered = first ? rememberedDelivery(first) : undefined;
      if (remembered && remembered !== 'messenger') {
        void doSend({ mode: remembered as PasswordDeliveryMode, rememberForRecipient: true });
        return;
      }
      askPassword = true;
      return;
    }
    void doSend();
  }

  async function doSend(pw?: PasswordDelivery) {
    askPassword = false;
    sending = true;
    error = null;
    try {
      if (pw?.rememberForRecipient) {
        const first = (to[0] ?? cc[0])?.email;
        if (first) rememberDelivery(first, pw.mode);
      }
      const outgoing: OutgoingAttachment[] = [];
      for (const a of attachments) {
        const token = a.file ? (await app.api.uploadAttachment(a.file, a.filename)).uploadToken : `existing:${a.existingId}`;
        outgoing.push({ uploadToken: token, filename: a.filename, mimeType: a.mimeType, size: a.size, delivery: a.delivery });
      }
      const html = editor?.innerHTML ?? '';
      const text = editor?.innerText ?? '';
      const res = await app.api.send({
        channel: 'email',
        identityId,
        to,
        cc,
        bcc,
        subject,
        html,
        text,
        inReplyToMessageId: start.inReplyToMessageId,
        forwardOfMessageId: start.forwardOfMessageId,
        attachments: outgoing,
        passwordDelivery: pw,
      });
      if (pw?.mode === 'none' && res.sharePasswords?.length) {
        try {
          await navigator.clipboard.writeText(res.sharePasswords.map((s) => `${s.filename}: ${s.password}`).join('\n'));
          app.toast(t('compose.passwordCopied'));
        } catch {
          app.toast(t('compose.passwordCopyFailed'), undefined, 'error');
        }
      }
      app.toast(res.queued ? t('compose.queued') : t('compose.sent'));
      onclose();
    } catch (err) {
      error = t('compose.sendFailed', { error: err instanceof Error ? err.message : String(err) });
    } finally {
      sending = false;
    }
  }

  function onkeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
      e.preventDefault();
      trySend();
    }
  }

  const deliveryOptions: AttachmentDelivery[] = ['attach', 'nextcloud', 'opencloud', 'vaultwarden'];
</script>

<Modal {title} onclose={onclose} width="860px">
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div
    class="composer"
    {onkeydown}
    ondragover={(e) => {
      e.preventDefault();
      dragOver = true;
    }}
    ondragleave={() => (dragOver = false)}
    ondrop={(e) => {
      e.preventDefault();
      dragOver = false;
      addFiles(e.dataTransfer?.files ?? null);
    }}
    class:drag={dragOver}
  >
    <div class="from">
      <label class="lbl" for="compose-from">{t('compose.from')}</label>
      <select id="compose-from" class="input" bind:value={identityId} onchange={onIdentityChange}>
        {#each app.mailAccounts as acc (acc.id)}
          <optgroup label={acc.displayName}>
            {#each app.identities.filter((i) => i.accountId === acc.id) as i (i.id)}
              <option value={i.id}>{i.name} &lt;{i.email}&gt;</option>
            {/each}
          </optgroup>
        {/each}
      </select>
      <div class="cc-toggles">
        {#if !showCc}<button class="btn btn-ghost sm" onclick={() => (showCc = true)}>Cc</button>{/if}
        {#if !showBcc}<button class="btn btn-ghost sm" onclick={() => (showBcc = true)}>Bcc</button>{/if}
      </div>
    </div>
    <RecipientInput label={t('compose.to')} bind:value={to} persons={app.persons} autofocus={start.mode === 'new' || start.mode === 'forward'} />
    {#if showCc}<RecipientInput label="Cc" bind:value={cc} persons={app.persons} />{/if}
    {#if showBcc}<RecipientInput label="Bcc" bind:value={bcc} persons={app.persons} />{/if}
    <div class="subject">
      <label class="lbl" for="compose-subject">{t('compose.subject')}</label>
      <input id="compose-subject" bind:value={subject} placeholder={t('compose.subjectPlaceholder')} />
    </div>

    <div class="toolbar" role="toolbar" aria-label={t('compose.formatting')}>
      <button class="icon-btn" title={t('compose.bold') + ' (Ctrl+B)'} aria-label={t('compose.bold')} onclick={() => exec('bold')}><Bold size={16} /></button>
      <button class="icon-btn" title={t('compose.italic') + ' (Ctrl+I)'} aria-label={t('compose.italic')} onclick={() => exec('italic')}><Italic size={16} /></button>
      <button class="icon-btn" title={t('compose.underline') + ' (Ctrl+U)'} aria-label={t('compose.underline')} onclick={() => exec('underline')}><Underline size={16} /></button>
      <span class="sep"></span>
      <button class="icon-btn" title={t('compose.bullets')} aria-label={t('compose.bullets')} onclick={() => exec('insertUnorderedList')}><List size={16} /></button>
      <button class="icon-btn" title={t('compose.numbers')} aria-label={t('compose.numbers')} onclick={() => exec('insertOrderedList')}><ListOrdered size={16} /></button>
      <button class="icon-btn" title={t('compose.quote')} aria-label={t('compose.quote')} onclick={() => exec('formatBlock', 'blockquote')}><Quote size={16} /></button>
      <button class="icon-btn" title={t('compose.link')} aria-label={t('compose.link')} onclick={addLink}><LinkIcon size={16} /></button>
      <span class="sep"></span>
      <button class="icon-btn" title={t('compose.insertSignature')} aria-label={t('compose.insertSignature')} onclick={insertSignature}><Signature size={16} /></button>
      <button class="icon-btn" title={t('compose.attach')} aria-label={t('compose.attach')} onclick={() => fileInput?.click()}><Paperclip size={16} /></button>
      <input type="file" multiple hidden bind:this={fileInput} onchange={(e) => addFiles((e.currentTarget as HTMLInputElement).files)} />
    </div>

    <div
      class="editor"
      bind:this={editor}
      contenteditable="true"
      role="textbox"
      tabindex="0"
      aria-multiline="true"
      aria-label={t('compose.body')}
      data-autofocus={isReply ? '' : undefined}
    ></div>

    {#if attachments.length}
      <div class="atts" aria-label={t('compose.attachments')}>
        <div class="atts-head">
          <span>{t('compose.attachmentsCount', { count: attachments.length })}</span>
          {#if totalAttachSize > 0}<span class="muted">· {formatSize(totalAttachSize)} {t('compose.direct')}</span>{/if}
        </div>
        {#each attachments as a, i (a.key)}
          <div class="att">
            <span class="att-icon">{#if a.delivery === 'attach'}<Paperclip size={15} />{:else}<Cloud size={15} />{/if}</span>
            <span class="att-name" title={a.filename}>{a.filename}</span>
            <span class="att-size">{formatSize(a.size)}</span>
            {#if a.size > LARGE_ATTACHMENT && a.delivery === 'attach'}
              <span class="warn">{t('compose.largeHint')}</span>
            {/if}
            <label class="sr-only" for="dlv-{a.key}">{t('compose.deliveryFor', { name: a.filename })}</label>
            <select id="dlv-{a.key}" class="input dlv" bind:value={a.delivery}>
              {#each deliveryOptions as d (d)}
                <option value={d}>{t(`compose.delivery.${d}`)}</option>
              {/each}
            </select>
            <button class="icon-btn" aria-label={t('compose.removeAttachment', { name: a.filename })} onclick={() => attachments.splice(i, 1)}><Trash size={15} /></button>
          </div>
        {/each}
      </div>
    {/if}
    {#if dragOver}<div class="dropzone">{t('compose.dropHere')}</div>{/if}
  </div>

  {#snippet footer()}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
    <span class="hint">{identity ? identity.email : ''}</span>
    <button class="btn" onclick={onclose}>{t('compose.discard')}</button>
    <button class="btn btn-primary" onclick={trySend} disabled={sending} title="Ctrl+Enter">
      <SendIcon size={15} />
      {sending ? t('compose.sending') : t('common.action.send')}
    </button>
  {/snippet}
</Modal>

{#if askPassword}
  <PasswordDeliveryDialog
    files={cloudAttachments.map((a) => a.filename)}
    recipientName={to[0]?.name || to[0]?.email || cc[0]?.email || ''}
    {messengerTargets}
    onconfirm={(d) => doSend(d)}
    oncancel={() => (askPassword = false)}
  />
{/if}

<style>
  .composer {
    position: relative;
    display: flex;
    flex-direction: column;
    min-height: 420px;
  }
  .from,
  .subject {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 4px 0;
    border-bottom: 1px solid var(--tm-line);
  }
  .from .input {
    border: none;
    padding: 0;
    height: 30px;
    box-shadow: none;
    flex: 1;
    min-width: 0;
  }
  .lbl {
    width: 44px;
    flex: none;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .cc-toggles {
    display: flex;
    gap: 2px;
  }
  .sm {
    height: 26px;
    padding: 0 8px;
    font-size: 12px;
  }
  .subject input {
    flex: 1;
    height: 32px;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-weight: 500;
    outline: none;
  }
  .toolbar {
    display: flex;
    align-items: center;
    gap: 2px;
    padding: 6px 0;
    border-bottom: 1px solid var(--tm-line);
    flex-wrap: wrap;
  }
  .toolbar .icon-btn {
    width: 30px;
    height: 30px;
  }
  .sep {
    width: 1px;
    height: 18px;
    background: var(--tm-line);
    margin: 0 4px;
  }
  .editor {
    flex: 1;
    min-height: 220px;
    max-height: 46vh;
    overflow: auto;
    padding: 12px 2px;
    outline: none;
    font-size: 14px;
    line-height: 1.55;
  }
  .editor :global(p) {
    margin: 0 0 8px;
  }
  .editor :global(.sig) {
    color: var(--tm-muted);
    margin: 12px 0;
  }
  .editor :global(.sig p) {
    margin: 0 0 4px;
  }
  .editor :global(blockquote) {
    margin: 4px 0 4px 2px;
    padding-left: 12px;
    border-left: 3px solid var(--tm-line-strong);
    color: var(--tm-muted);
  }
  .atts {
    border-top: 1px solid var(--tm-line);
    padding-top: 8px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .atts-head {
    font-size: 12px;
    font-weight: 600;
    color: var(--tm-muted);
  }
  .muted {
    font-weight: 400;
  }
  .att {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 6px 6px 10px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
  }
  .att-icon {
    display: inline-flex;
    color: var(--tm-muted);
  }
  .att-name {
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: 13px;
    font-weight: 500;
  }
  .att-size {
    font-size: 12px;
    color: var(--tm-muted);
  }
  .warn {
    font-size: 11.5px;
    color: var(--tm-warn);
  }
  .dlv {
    width: 170px;
    height: 30px;
    font-size: 12.5px;
  }
  .dropzone {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    border: 2px dashed var(--tm-accent);
    border-radius: 12px;
    background: color-mix(in srgb, var(--tm-accent-soft) 85%, transparent);
    color: var(--tm-accent-text);
    font-weight: 600;
    pointer-events: none;
  }
  .error {
    margin: 0;
    flex: 1;
    color: var(--tm-danger);
    font-size: 13px;
    align-self: center;
  }
  .hint {
    flex: 1;
    align-self: center;
    font-size: 12px;
    color: var(--tm-faint);
  }
  .error + .hint {
    display: none;
  }
  @media (max-width: 639px) {
    .att {
      flex-wrap: wrap;
    }
    .dlv {
      width: 100%;
    }
  }
</style>
