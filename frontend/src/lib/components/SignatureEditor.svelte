<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  WYSIWYG editor for HTML signatures (KONZEPT 8.2): formatting, links
  (http/https/mailto/tel), images (upload, paste, drag & drop, resize by
  width, remote https URLs), an HTML source mode for signature generators,
  live previews on light and dark backgrounds and the plain-text fallback
  (generated, or written by hand). Pasted HTML is cleaned with an allowlist
  before it reaches the DOM; the server sanitizes again on save and send.
-->
<script lang="ts" module>
  export interface SignatureValue {
    html: string;
    plain: string;
    plainOverride: boolean;
  }
</script>

<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import Bold from '@lucide/svelte/icons/letter-b';
  import Italic from '@lucide/svelte/icons/italic';
  import Underline from '@lucide/svelte/icons/underline';
  import List from '@lucide/svelte/icons/list';
  import ListOrdered from '@lucide/svelte/icons/list-ordered';
  import AlignLeft from '@lucide/svelte/icons/text-align-start';
  import AlignCenter from '@lucide/svelte/icons/text-align-center';
  import AlignRight from '@lucide/svelte/icons/text-align-end';
  import LinkIcon from '@lucide/svelte/icons/link';
  import Unlink from '@lucide/svelte/icons/unlink';
  import ImagePlus from '@lucide/svelte/icons/image-plus';
  import ImageIcon from '@lucide/svelte/icons/image';
  import CodeXml from '@lucide/svelte/icons/code-xml';
  import RemoveFormatting from '@lucide/svelte/icons/remove-formatting';
  import Trash from '@lucide/svelte/icons/trash';
  import { formatSize, t } from '../i18n/index.svelte';
  import {
    MAX_SIGNATURE_IMAGE,
    SIGNATURE_IMAGE_TYPES,
    SignatureImageError,
    applyLink,
    cleanSignatureHtml,
    htmlToPlain,
    insertImage,
    linkAt,
    normalizeLink,
    previewDocument,
    readImageFile,
    removeLink,
    setImageWidth,
  } from '../signature';

  interface Props {
    html: string;
    plain: string;
    plainOverride?: boolean;
    /** Accessible name of the editing area. */
    label?: string;
    /** Called when the user finished a change (blur, toolbar action, source applied). */
    onchange: (value: SignatureValue) => void;
  }
  let { html, plain, plainOverride = false, label, onchange }: Props = $props();

  const uid = Math.random().toString(36).slice(2, 8);
  let editor = $state<HTMLDivElement>();
  let fileInput = $state<HTMLInputElement>();
  let mode = $state<'visual' | 'source'>('visual');
  let current = $state(untrack(() => cleanSignatureHtml(html).html));
  let source = $state('');
  let override = $state(untrack(() => plainOverride));
  let ownPlain = $state(untrack(() => plain));
  let error = $state<string | null>(null);
  let notice = $state<string | null>(null);
  let panel = $state<'link' | 'image' | null>(null);
  let linkUrl = $state('');
  let linkText = $state('');
  let editingLink = $state(false);
  let imageUrl = $state('');
  let selectedImg = $state<HTMLImageElement | null>(null);
  let imgWidth = $state(0);
  let savedRange: Range | null = null;
  let lastCommitted = untrack(() => JSON.stringify({ html: current, plain, plainOverride }));

  const autoPlain = $derived(htmlToPlain(current));
  const remoteImages = $derived((current.match(/<img[^>]+src="https:/gi) ?? []).length);
  const sizeLimit = formatSize(MAX_SIGNATURE_IMAGE);

  onMount(() => {
    if (editor) editor.innerHTML = current;
    const onSel = () => {
      const sel = document.getSelection();
      if (editor && sel && sel.rangeCount && editor.contains(sel.getRangeAt(0).commonAncestorContainer)) savedRange = sel.getRangeAt(0).cloneRange();
    };
    document.addEventListener('selectionchange', onSel);
    return () => document.removeEventListener('selectionchange', onSel);
  });

  // Saved values from outside (e.g. after the server sanitized them).
  $effect(() => {
    const incoming = cleanSignatureHtml(html).html;
    const po = plainOverride;
    const p = plain;
    untrack(() => {
      if (editor && document.activeElement !== editor && mode === 'visual' && incoming !== current) {
        editor.innerHTML = incoming;
        current = incoming;
      }
      if (document.activeElement?.id !== `sig-plain-${uid}`) {
        override = po;
        ownPlain = p;
      }
      lastCommitted = JSON.stringify({ html: current, plain: p, plainOverride: po });
    });
  });

  function sync() {
    if (editor) current = editor.innerHTML;
  }

  function commit() {
    sync();
    const value: SignatureValue = { html: current, plain: override ? ownPlain : autoPlain, plainOverride: override };
    const key = JSON.stringify(value);
    if (key === lastCommitted) return;
    lastCommitted = key;
    onchange(value);
  }

  function restoreRange(): Range | null {
    if (!editor) return null;
    editor.focus();
    const sel = document.getSelection();
    if (savedRange && editor.contains(savedRange.commonAncestorContainer) && sel) {
      sel.removeAllRanges();
      sel.addRange(savedRange);
      return savedRange;
    }
    const r = document.createRange();
    r.selectNodeContents(editor);
    r.collapse(false);
    sel?.removeAllRanges();
    sel?.addRange(r);
    return r;
  }

  function exec(cmd: string, value?: string) {
    restoreRange();
    document.execCommand('styleWithCSS', false, 'false');
    document.execCommand(cmd, false, value);
    commit();
  }

  // ---- links ----------------------------------------------------------------
  function openLink() {
    error = null;
    const r = savedRange;
    const a = r && editor ? linkAt(r.commonAncestorContainer, editor) : null;
    editingLink = !!a;
    linkUrl = a?.getAttribute('href') ?? '';
    linkText = a?.textContent ?? r?.toString() ?? '';
    panel = 'link';
  }

  function submitLink(e: SubmitEvent) {
    e.preventDefault();
    if (!editor) return;
    if (!normalizeLink(linkUrl)) {
      error = t('settings.signatures.editor.linkInvalid');
      return;
    }
    const r = restoreRange();
    if (r && applyLink(editor, r, linkUrl, linkText)) {
      panel = null;
      error = null;
      commit();
    }
  }

  function unlink() {
    const r = restoreRange();
    if (editor && r && removeLink(editor, r)) commit();
    panel = null;
  }

  // ---- images ---------------------------------------------------------------
  function imageError(err: unknown) {
    error =
      err instanceof SignatureImageError
        ? err.reason === 'size'
          ? t('settings.signatures.editor.imageTooLarge', { size: sizeLimit })
          : t('settings.signatures.editor.imageType')
        : String(err);
  }

  async function addImageFiles(files: Iterable<File>) {
    error = null;
    for (const f of files) {
      try {
        const uri = await readImageFile(f);
        if (!editor) return;
        insertImage(editor, restoreRange(), uri, f.name.replace(/\.[a-z0-9]+$/i, ''), 160);
      } catch (err) {
        imageError(err);
      }
    }
    commit();
  }

  function submitImageUrl(e: SubmitEvent) {
    e.preventDefault();
    if (!editor) return;
    const u = imageUrl.trim();
    if (!/^https:\/\/\S+$/i.test(u)) {
      error = t('settings.signatures.editor.imageUrlInvalid');
      return;
    }
    insertImage(editor, restoreRange(), u, '', 160);
    imageUrl = '';
    panel = null;
    error = null;
    commit();
  }

  function onEditorClick(e: MouseEvent) {
    const el = e.target as HTMLElement;
    if (el.tagName === 'IMG') {
      selectedImg = el as HTMLImageElement;
      imgWidth = Number(selectedImg.getAttribute('width')) || selectedImg.naturalWidth || selectedImg.width || 160;
    } else {
      selectedImg = null;
    }
  }

  function resizeImage(w: number) {
    if (!selectedImg || !Number.isFinite(w) || w <= 0) return;
    setImageWidth(selectedImg, w);
    imgWidth = Number(selectedImg.getAttribute('width'));
    sync();
  }

  function removeImage() {
    selectedImg?.remove();
    selectedImg = null;
    commit();
  }

  // ---- paste / drop ---------------------------------------------------------
  function onPaste(e: ClipboardEvent) {
    const dt = e.clipboardData;
    if (!dt) return;
    const files = [...dt.files].filter((f) => f.type.startsWith('image/'));
    if (files.length) {
      e.preventDefault();
      void addImageFiles(files);
      return;
    }
    const pasted = dt.getData('text/html');
    if (pasted) {
      e.preventDefault();
      const cleaned = cleanSignatureHtml(pasted);
      if (cleaned.oversized) notice = t('settings.signatures.editor.oversized', { size: sizeLimit });
      document.execCommand('insertHTML', false, cleaned.html);
      sync();
    }
  }

  function onDrop(e: DragEvent) {
    const files = [...(e.dataTransfer?.files ?? [])].filter((f) => f.type.startsWith('image/'));
    if (!files.length) return;
    e.preventDefault();
    e.stopPropagation();
    const pos = document.caretRangeFromPoint?.(e.clientX, e.clientY);
    if (pos && editor?.contains(pos.commonAncestorContainer)) savedRange = pos;
    void addImageFiles(files);
  }

  // ---- source mode ----------------------------------------------------------
  function toggleSource() {
    error = null;
    notice = null;
    if (mode === 'visual') {
      sync();
      source = current;
      selectedImg = null;
      panel = null;
      mode = 'source';
      return;
    }
    applySource();
    mode = 'visual';
  }

  function applySource() {
    const cleaned = cleanSignatureHtml(source);
    if (cleaned.oversized) notice = t('settings.signatures.editor.oversized', { size: sizeLimit });
    current = cleaned.html;
    if (editor) editor.innerHTML = cleaned.html;
    commit();
  }

  function setOverride(on: boolean) {
    override = on;
    if (on && !ownPlain.trim()) ownPlain = autoPlain;
    commit();
  }
</script>

<div class="sig-editor">
  <div class="toolbar" role="toolbar" aria-label={t('settings.signatures.editor.toolbar')}>
    {#if mode === 'visual'}
      <button type="button" class="icon-btn" title={t('compose.bold')} aria-label={t('compose.bold')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('bold')}><Bold size={15} /></button>
      <button type="button" class="icon-btn" title={t('compose.italic')} aria-label={t('compose.italic')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('italic')}><Italic size={15} /></button>
      <button type="button" class="icon-btn" title={t('compose.underline')} aria-label={t('compose.underline')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('underline')}><Underline size={15} /></button>
      <select
        class="input size"
        aria-label={t('settings.signatures.editor.fontSize')}
        title={t('settings.signatures.editor.fontSize')}
        value=""
        onchange={(e) => {
          const v = (e.currentTarget as HTMLSelectElement).value;
          (e.currentTarget as HTMLSelectElement).value = '';
          if (v) exec('fontSize', v);
        }}
      >
        <option value="">{t('settings.signatures.editor.fontSize')}</option>
        <option value="1">{t('settings.signatures.editor.sizeTiny')}</option>
        <option value="2">{t('settings.signatures.editor.sizeSmall')}</option>
        <option value="3">{t('settings.signatures.editor.sizeNormal')}</option>
        <option value="4">{t('settings.signatures.editor.sizeLarge')}</option>
        <option value="5">{t('settings.signatures.editor.sizeHuge')}</option>
      </select>
      <label class="color" title={t('settings.signatures.editor.color')}>
        <span class="sr-only">{t('settings.signatures.editor.color')}</span>
        <input type="color" value="#1f2328" onchange={(e) => exec('foreColor', (e.currentTarget as HTMLInputElement).value)} />
      </label>
      <span class="sep"></span>
      <button type="button" class="icon-btn" title={t('settings.signatures.editor.alignLeft')} aria-label={t('settings.signatures.editor.alignLeft')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('justifyLeft')}><AlignLeft size={15} /></button>
      <button type="button" class="icon-btn" title={t('settings.signatures.editor.alignCenter')} aria-label={t('settings.signatures.editor.alignCenter')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('justifyCenter')}><AlignCenter size={15} /></button>
      <button type="button" class="icon-btn" title={t('settings.signatures.editor.alignRight')} aria-label={t('settings.signatures.editor.alignRight')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('justifyRight')}><AlignRight size={15} /></button>
      <button type="button" class="icon-btn" title={t('compose.bullets')} aria-label={t('compose.bullets')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('insertUnorderedList')}><List size={15} /></button>
      <button type="button" class="icon-btn" title={t('compose.numbers')} aria-label={t('compose.numbers')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('insertOrderedList')}><ListOrdered size={15} /></button>
      <span class="sep"></span>
      <button type="button" class="icon-btn" class:on={panel === 'link'} title={t('settings.signatures.editor.link')} aria-label={t('settings.signatures.editor.link')} onmousedown={(e) => e.preventDefault()} onclick={openLink}><LinkIcon size={15} /></button>
      <button type="button" class="icon-btn" title={t('settings.signatures.editor.linkRemove')} aria-label={t('settings.signatures.editor.linkRemove')} onmousedown={(e) => e.preventDefault()} onclick={unlink}><Unlink size={15} /></button>
      <button type="button" class="icon-btn" title={t('settings.signatures.editor.imageUpload')} aria-label={t('settings.signatures.editor.imageUpload')} onmousedown={(e) => e.preventDefault()} onclick={() => fileInput?.click()}><ImagePlus size={15} /></button>
      <button type="button" class="icon-btn" class:on={panel === 'image'} title={t('settings.signatures.editor.imageUrl')} aria-label={t('settings.signatures.editor.imageUrl')} onmousedown={(e) => e.preventDefault()} onclick={() => (panel = panel === 'image' ? null : 'image')}><ImageIcon size={15} /></button>
      <button type="button" class="icon-btn" title={t('settings.signatures.editor.clearFormat')} aria-label={t('settings.signatures.editor.clearFormat')} onmousedown={(e) => e.preventDefault()} onclick={() => exec('removeFormat')}><RemoveFormatting size={15} /></button>
    {/if}
    <span class="grow"></span>
    <button type="button" class="btn btn-ghost sm" aria-pressed={mode === 'source'} onclick={toggleSource}>
      <CodeXml size={14} />
      {mode === 'source' ? t('settings.signatures.editor.visual') : t('settings.signatures.editor.source')}
    </button>
    <input type="file" hidden accept={SIGNATURE_IMAGE_TYPES.join(',')} multiple bind:this={fileInput} onchange={(e) => {
      const input = e.currentTarget as HTMLInputElement;
      void addImageFiles([...(input.files ?? [])]);
      input.value = '';
    }} />
  </div>

  {#if panel === 'link'}
    <form class="panel" onsubmit={submitLink}>
      <label>
        <span class="lbl">{t('settings.signatures.editor.linkUrl')}</span>
        <input class="input" bind:value={linkUrl} placeholder="https://" />
      </label>
      <label>
        <span class="lbl">{t('settings.signatures.editor.linkText')}</span>
        <input class="input" bind:value={linkText} />
      </label>
      <button class="btn btn-primary sm" type="submit">{editingLink ? t('settings.signatures.editor.linkUpdate') : t('settings.signatures.editor.linkInsert')}</button>
      <button class="btn sm" type="button" onclick={() => (panel = null)}>{t('common.action.cancel')}</button>
    </form>
  {:else if panel === 'image'}
    <form class="panel" onsubmit={submitImageUrl}>
      <label class="wide">
        <span class="lbl">{t('settings.signatures.editor.imageUrl')}</span>
        <input class="input" bind:value={imageUrl} placeholder="https://" />
      </label>
      <button class="btn btn-primary sm" type="submit">{t('settings.signatures.editor.imageInsert')}</button>
      <button class="btn sm" type="button" onclick={() => (panel = null)}>{t('common.action.cancel')}</button>
      <p class="hint full">{t('settings.signatures.editor.remoteHint')}</p>
    </form>
  {/if}

  <div
      class="area"
      hidden={mode !== 'visual'}
      bind:this={editor}
      contenteditable="true"
      role="textbox"
      tabindex="0"
      aria-multiline="true"
      aria-label={label ?? t('settings.signatures.editor.area')}
      oninput={sync}
      onblur={commit}
      onpaste={onPaste}
      ondrop={onDrop}
      ondragover={(e) => e.preventDefault()}
      onclick={onEditorClick}
      onkeydown={(e) => {
        if (e.key === 'Escape') selectedImg = null;
      }}
    ></div>
  {#if mode === 'source'}
    <textarea
      class="input source"
      rows="10"
      spellcheck="false"
      aria-label={t('settings.signatures.editor.source')}
      bind:value={source}
      onblur={applySource}
    ></textarea>
    <p class="hint">{t('settings.signatures.editor.sourceHint')}</p>
  {/if}

  {#if selectedImg && mode === 'visual'}
    <div class="panel img-tools">
      <label class="wide">
        <span class="lbl">{t('settings.signatures.editor.imageWidth', { width: imgWidth })}</span>
        <input type="range" min="16" max="600" value={imgWidth} oninput={(e) => resizeImage(Number((e.currentTarget as HTMLInputElement).value))} onchange={commit} />
      </label>
      <input class="input num" type="number" min="16" max="800" aria-label={t('settings.signatures.editor.imageWidthPx')} value={imgWidth} onchange={(e) => {
        resizeImage(Number((e.currentTarget as HTMLInputElement).value));
        commit();
      }} />
      <button type="button" class="icon-btn" title={t('settings.signatures.editor.imageRemove')} aria-label={t('settings.signatures.editor.imageRemove')} onclick={removeImage}><Trash size={15} /></button>
    </div>
  {/if}

  {#if error}<p class="msg error" role="alert">{error}</p>{/if}
  {#if notice}<p class="msg" role="status">{notice}</p>{/if}
  {#if remoteImages > 0}<p class="hint">{t('settings.signatures.editor.remoteCount', { count: remoteImages })}</p>{/if}

  <details class="preview">
    <summary>{t('settings.signatures.editor.preview')}</summary>
    <div class="frames">
      <figure>
        <figcaption>{t('settings.signatures.editor.previewLight')}</figcaption>
        <iframe sandbox="" title={t('settings.signatures.editor.previewLight')} srcdoc={previewDocument(current, false)}></iframe>
      </figure>
      <figure>
        <figcaption>{t('settings.signatures.editor.previewDark')}</figcaption>
        <iframe sandbox="" title={t('settings.signatures.editor.previewDark')} srcdoc={previewDocument(current, true)}></iframe>
      </figure>
    </div>
  </details>

  <details class="plain" open={override}>
    <summary>{t('settings.signatures.editor.plain')}</summary>
    <label class="check">
      <input type="checkbox" checked={override} onchange={(e) => setOverride((e.currentTarget as HTMLInputElement).checked)} />
      {t('settings.signatures.editor.plainOverride')}
    </label>
    {#if override}
      <textarea id="sig-plain-{uid}" class="input mono" rows="4" aria-label={t('settings.signatures.editor.plain')} bind:value={ownPlain} onblur={commit}></textarea>
    {:else}
      <p class="hint">{t('settings.signatures.editor.plainAuto')}</p>
      <pre class="auto">{autoPlain}</pre>
    {/if}
  </details>
</div>

<style>
  .sig-editor {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 4px;
  }
  .toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 2px;
    padding: 3px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface-2, var(--tm-surface));
  }
  .toolbar .on {
    background: var(--tm-accent-soft, var(--tm-line));
  }
  .sep {
    width: 1px;
    height: 18px;
    margin: 0 4px;
    background: var(--tm-line);
  }
  .grow {
    flex: 1;
  }
  .size {
    width: auto;
    height: 28px;
    padding: 0 6px;
    font-size: 12px;
  }
  .color {
    display: inline-flex;
    align-items: center;
    padding: 0 4px;
  }
  .color input {
    width: 26px;
    height: 22px;
    padding: 0;
    border: 1px solid var(--tm-line);
    border-radius: 4px;
    background: none;
    cursor: pointer;
  }
  .area {
    min-height: 90px;
    max-height: 340px;
    overflow: auto;
    padding: 8px 10px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface);
    font-size: 13px;
    line-height: 1.45;
    outline: none;
  }
  .area[hidden] {
    display: none;
  }
  .area:focus {
    border-color: var(--tm-accent);
  }
  .area :global(p) {
    margin: 0 0 4px;
  }
  .area :global(img) {
    cursor: pointer;
    max-width: 100%;
  }
  .area :global(table) {
    border-collapse: collapse;
  }
  .source,
  .mono {
    height: auto;
    padding: 6px 10px;
    font-family: var(--tm-mono);
    font-size: 12px;
    resize: vertical;
  }
  .panel {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 6px;
    padding: 8px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
  }
  .panel label {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 160px;
    flex: 1;
  }
  .panel .wide {
    flex: 3;
  }
  .full {
    flex-basis: 100%;
  }
  .lbl {
    font-size: 11.5px;
    color: var(--tm-muted);
  }
  .num {
    width: 80px;
  }
  .hint,
  .msg {
    margin: 0;
    font-size: 12px;
    color: var(--tm-muted);
  }
  .error {
    color: var(--tm-danger, #c62828);
  }
  details summary {
    cursor: pointer;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
  .frames {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 8px;
    margin-top: 6px;
  }
  figure {
    margin: 0;
  }
  figcaption {
    font-size: 11.5px;
    color: var(--tm-muted);
    margin-bottom: 2px;
  }
  iframe {
    width: 100%;
    height: 150px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    resize: vertical;
  }
  .check {
    display: flex;
    align-items: center;
    gap: 6px;
    margin: 6px 0;
    font-size: 12.5px;
  }
  .auto {
    margin: 0;
    padding: 6px 10px;
    white-space: pre-wrap;
    font-family: var(--tm-mono);
    font-size: 12px;
    border: 1px dashed var(--tm-line);
    border-radius: var(--tm-radius);
    color: var(--tm-muted);
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }
</style>
