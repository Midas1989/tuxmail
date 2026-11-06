<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Composer toolbar additions: spell checking with underlines (CSS Custom
  Highlight API) and a context menu with suggestions / "add to dictionary",
  video conference link, S/MIME / OpenPGP choice, mail merge.
-->
<script lang="ts">
  import SpellCheck from '@lucide/svelte/icons/spell-check';
  import Video from '@lucide/svelte/icons/video';
  import Lock from '@lucide/svelte/icons/lock';
  import LockOpen from '@lucide/svelte/icons/lock-open';
  import Mails from '@lucide/svelte/icons/mails';
  import { app, comfort } from '../../state';
  import { i18n, t } from '../../i18n/index.svelte';
  import type { Address } from '../../types';
  import type { MeetingProvider, Misspelling, OutgoingCrypto, PgpStatus, SmimeStatus } from '../../api/comfort';
  import ContextMenu, { type MenuItem } from '../common/ContextMenu.svelte';
  import MergeWizard from './MergeWizard.svelte';

  interface Props {
    editor: HTMLDivElement | undefined;
    identityId: number;
    fromEmail: string;
    recipients: Address[];
    subject: string;
    crypto: OutgoingCrypto;
  }
  let { editor, identityId, fromEmail, recipients, subject, crypto = $bindable() }: Props = $props();

  // ---- spell checking ---------------------------------------------------------
  let spellOn = $state(true);
  let lang = $state('auto');
  let detected = $state('');
  let errors = $state<{ m: Misspelling; range: Range }[]>([]);
  let menu = $state<{ x: number; y: number; items: MenuItem[] } | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let seq = 0;
  const HL = 'tm-spell';
  const supported = typeof CSS !== 'undefined' && 'highlights' in CSS && typeof Highlight !== 'undefined';

  $effect(() => {
    void comfort.loadSettings().then((s) => {
      if (s) {
        spellOn = s.spell.enabled;
        lang = s.spell.language || 'auto';
      }
    });
  });

  interface Seg {
    node: Text;
    start: number;
  }

  function collect(root: HTMLElement): { text: string; segs: Seg[] } {
    const segs: Seg[] = [];
    let text = '';
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
      acceptNode: (n) => ((n.parentElement?.closest('[data-quote], [data-signature], blockquote, a, code, pre') ? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT)),
    });
    for (let n = walker.nextNode() as Text | null; n; n = walker.nextNode() as Text | null) {
      segs.push({ node: n, start: text.length });
      text += n.data + '\n';
    }
    return { text, segs };
  }

  async function check() {
    if (!editor || !spellOn) return clear();
    const my = ++seq;
    const { text, segs } = collect(editor);
    if (!text.trim()) return clear();
    try {
      const res = await comfort.api.spellCheck(text, lang);
      if (my !== seq) return;
      detected = res.language;
      const out: { m: Misspelling; range: Range }[] = [];
      for (const m of res.misspellings) {
        const seg = [...segs].reverse().find((s) => s.start <= m.start);
        if (!seg || m.end - seg.start > seg.node.data.length || !seg.node.isConnected) continue;
        const r = document.createRange();
        r.setStart(seg.node, m.start - seg.start);
        r.setEnd(seg.node, m.end - seg.start);
        out.push({ m, range: r });
      }
      errors = out;
      paint();
    } catch {
      /* spell checking unavailable */
    }
  }

  function paint() {
    if (!supported) return;
    if (!errors.length) CSS.highlights.delete(HL);
    else CSS.highlights.set(HL, new Highlight(...errors.map((e) => e.range)));
  }

  function clear() {
    errors = [];
    if (supported) CSS.highlights.delete(HL);
  }

  function schedule() {
    clearTimeout(timer);
    timer = setTimeout(check, 700);
  }

  $effect(() => {
    const ed = editor;
    if (!ed) return;
    ed.spellcheck = !spellOn; // our checker replaces the browser's
    const onInput = () => schedule();
    const onMenu = (e: MouseEvent) => contextMenu(e);
    ed.addEventListener('input', onInput);
    ed.addEventListener('contextmenu', onMenu);
    schedule();
    return () => {
      ed.removeEventListener('input', onInput);
      ed.removeEventListener('contextmenu', onMenu);
      clearTimeout(timer);
      if (supported) CSS.highlights.delete(HL);
    };
  });

  function caretAt(x: number, y: number): { node: Node; offset: number } | null {
    const d = document as Document & { caretPositionFromPoint?: (x: number, y: number) => { offsetNode: Node; offset: number } | null };
    if (d.caretPositionFromPoint) {
      const p = d.caretPositionFromPoint(x, y);
      return p ? { node: p.offsetNode, offset: p.offset } : null;
    }
    const r = document.caretRangeFromPoint?.(x, y);
    return r ? { node: r.startContainer, offset: r.startOffset } : null;
  }

  function contextMenu(e: MouseEvent) {
    if (!spellOn || !errors.length) return;
    const p = caretAt(e.clientX, e.clientY);
    if (!p) return;
    const hit = errors.find((er) => {
      try {
        return er.range.startContainer === p.node && er.range.startOffset <= p.offset && p.offset <= er.range.endOffset;
      } catch {
        return false;
      }
    });
    if (!hit) return;
    e.preventDefault();
    const items: MenuItem[] = hit.m.suggestions.slice(0, 6).map((s, i) => ({ id: 's' + i, label: s, run: () => replace(hit.range, s) }));
    if (!items.length) items.push({ id: 'none', label: t('comfort.spell.noSuggestions'), disabled: true, run: () => {} });
    items.push({ id: 'add', label: t('comfort.spell.addToDictionary', { word: hit.m.word }), run: () => addWord(hit.m.word) });
    items.push({ id: 'ignore', label: t('comfort.spell.ignore'), run: () => ignore(hit.m.word) });
    menu = { x: e.clientX, y: e.clientY, items };
  }

  function replace(r: Range, s: string) {
    r.deleteContents();
    r.insertNode(document.createTextNode(s));
    editor?.normalize();
    editor?.dispatchEvent(new Event('input', { bubbles: true }));
  }

  async function addWord(w: string) {
    await comfort.api.addSpellWord(w);
    ignore(w);
    app.toast(t('comfort.spell.added', { word: w }));
  }

  function ignore(w: string) {
    errors = errors.filter((e) => e.m.word !== w);
    paint();
  }

  function toggleSpell() {
    spellOn = !spellOn;
    if (editor) editor.spellcheck = !spellOn;
    if (spellOn) void check();
    else clear();
  }

  // ---- video conference link ----------------------------------------------------
  let videoOpen = $state(false);
  let creating = $state(false);

  async function addMeeting(provider: MeetingProvider) {
    videoOpen = false;
    creating = true;
    try {
      const l = await comfort.api.createMeeting({ provider, title: subject || t('comfort.meeting.defaultTitle'), participants: recipients.map((r) => r.email), lang: i18n.locale });
      if (!editor) return;
      const esc = (v: string) => v.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!);
      const p = document.createElement('p');
      p.innerHTML = `🎥 <a href="${esc(l.url)}">${esc(t('comfort.meeting.join', { provider: l.label }))}</a>${l.password ? ` – ${esc(t('comfort.meeting.password', { password: l.password }))}` : ''}`;
      editor.insertBefore(p, editor.querySelector('[data-signature]') ?? editor.querySelector('[data-quote]'));
    } catch (e) {
      app.toast(t('comfort.meeting.failed', { error: e instanceof Error ? e.message : String(e) }), undefined, 'error');
    } finally {
      creating = false;
    }
  }

  // ---- S/MIME / OpenPGP ---------------------------------------------------------
  let status = $state<SmimeStatus | null>(null);
  let pgp = $state<PgpStatus | null>(null);
  $effect(() => {
    const from = fromEmail;
    const rc = recipients.map((r) => r.email);
    if (!from) return;
    const h = setTimeout(() => {
      void comfort.api.smimeStatus(from, rc).then((s) => (status = s)).catch(() => (status = null));
      void refreshPgp(from, rc);
    }, 300);
    return () => clearTimeout(h);
  });

  function refreshPgp(from: string, rc: string[]) {
    return comfort.api.pgpStatus(from, rc).then((s) => (pgp = s)).catch(() => (pgp = null));
  }

  // A key directory only hears about a recipient after this click.
  let lookupBusy = $state(false);
  let lookupNone = $state<string[]>([]);
  async function lookupMissing() {
    const missing = pgp?.missing ?? [];
    if (!missing.length || !confirm(t('comfort.crypto.pgpLookupConfirm', { recipients: missing.join(', ') }))) return;
    lookupBusy = true;
    const none: string[] = [];
    for (const email of missing) {
      const k = await comfort.api.lookupPgpKey(email, 'wkd').catch(() => comfort.api.lookupPgpKey(email, 'keyserver').catch(() => null));
      if (!k) none.push(email);
    }
    lookupNone = none;
    lookupBusy = false;
    await refreshPgp(fromEmail, recipients.map((r) => r.email));
  }

  type CryptoChoice = 'auto' | 'smime-sign' | 'smime-encrypt' | 'pgp-sign' | 'pgp-encrypt' | 'none';
  let choice = $state<CryptoChoice>('auto');
  function onCrypto() {
    switch (choice) {
      case 'smime-sign':
        crypto = { method: 'smime', sign: true, encrypt: false };
        break;
      case 'smime-encrypt':
        crypto = { method: 'smime', sign: true, encrypt: true };
        break;
      case 'pgp-sign':
        crypto = { method: 'pgp', sign: true, encrypt: false };
        break;
      case 'pgp-encrypt':
        crypto = { method: 'pgp', sign: true, encrypt: true };
        break;
      case 'none':
        crypto = { method: 'none' };
        break;
      default:
        crypto = { method: 'auto' };
    }
  }

  // ---- mail merge -------------------------------------------------------------------
  let mergeOpen = $state(false);
</script>

<span class="sep"></span>
<button class="icon-btn" class:on={spellOn} title={t('comfort.spell.toggle')} aria-label={t('comfort.spell.toggle')} aria-pressed={spellOn} onclick={toggleSpell}><SpellCheck size={16} /></button>
{#if spellOn}
  <label class="sr-only" for="spell-lang">{t('comfort.spell.language')}</label>
  <select id="spell-lang" class="mini" bind:value={lang} onchange={check} title={t('comfort.spell.language')}>
    <option value="auto">{detected ? t('comfort.spell.autoDetected', { lang: detected.toUpperCase() }) : t('comfort.spell.auto')}</option>
    <option value="de">{t('comfort.spell.lang.de')}</option>
    <option value="en">{t('comfort.spell.lang.en')}</option>
  </select>
  {#if errors.length}<span class="count" title={t('comfort.spell.errors', { count: errors.length })}>{errors.length}</span>{/if}
{/if}
<span class="wrap">
  <button class="icon-btn" title={t('comfort.meeting.add')} aria-label={t('comfort.meeting.add')} aria-expanded={videoOpen} disabled={creating} onclick={() => (videoOpen = !videoOpen)}><Video size={16} /></button>
  {#if videoOpen}
    <div class="pop" role="menu">
      <button role="menuitem" onclick={() => addMeeting('jitsi')}>{t('comfort.meeting.provider.jitsi')}</button>
      <button role="menuitem" onclick={() => addMeeting('nextcloud_talk')}>{t('comfort.meeting.provider.nextcloud_talk')}</button>
      <button role="menuitem" onclick={() => addMeeting('teams')}>{t('comfort.meeting.provider.teams')}</button>
    </div>
  {/if}
</span>
<span class="crypto" title={t('comfort.crypto.title')}>
  {#if crypto.encrypt}<Lock size={15} aria-hidden="true" />{:else}<LockOpen size={15} aria-hidden="true" />{/if}
  <label class="sr-only" for="crypto-sel">{t('comfort.crypto.title')}</label>
  <select id="crypto-sel" class="mini" bind:value={choice} onchange={onCrypto}>
    <option value="auto">{t('comfort.crypto.auto')}</option>
    <option value="smime-sign" disabled={status ? !status.canSign : false}>{t('comfort.crypto.smimeSign')}</option>
    <option value="smime-encrypt" disabled={status ? !status.canEncrypt : false}>{t('comfort.crypto.smimeEncrypt')}</option>
    <option value="pgp-sign" disabled={pgp ? !pgp.canSign : false}>{t('comfort.crypto.pgpSign')}</option>
    <option value="pgp-encrypt" disabled={pgp ? !pgp.canSign : false}>{t('comfort.crypto.pgpEncrypt')}</option>
    <option value="none">{t('comfort.crypto.none')}</option>
  </select>
  {#if choice === 'pgp-encrypt' && pgp?.missing?.length}
    <span class="missing" role="alert">{t('comfort.crypto.pgpMissing', { recipients: pgp.missing.join(', ') })}</span>
    <button type="button" class="btn btn-ghost mini" disabled={lookupBusy} onclick={lookupMissing}>{t('comfort.crypto.pgpLookup')}</button>
    {#if lookupNone.length}<span class="missing">{t('comfort.crypto.pgpLookupNone', { recipients: lookupNone.join(', ') })}</span>{/if}
  {/if}
</span>
<button class="icon-btn" title={t('comfort.merge.open')} aria-label={t('comfort.merge.open')} onclick={() => (mergeOpen = true)}><Mails size={16} /></button>

{#if menu}
  <ContextMenu x={menu.x} y={menu.y} items={menu.items} label={t('comfort.spell.menu')} onclose={() => (menu = null)} />
{/if}
{#if mergeOpen}
  <MergeWizard identityId={identityId} subject={subject} html={editor?.innerHTML ?? ''} text={editor?.innerText ?? ''} onclose={() => (mergeOpen = false)} />
{/if}

<style>
  .sep {
    width: 1px;
    height: 20px;
    background: var(--tm-line);
    margin: 0 6px;
  }
  .on {
    color: var(--tm-accent-text);
  }
  .mini {
    width: auto;
    max-width: 190px;
    flex: none;
    height: 26px;
    border: 1px solid var(--tm-line);
    border-radius: 6px;
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    font-size: 12px;
    padding: 0 4px;
  }
  .count {
    font-size: 11px;
    min-width: 16px;
    padding: 0 5px;
    border-radius: 8px;
    background: color-mix(in srgb, #dc2626 18%, transparent);
    color: #b91c1c;
    text-align: center;
  }
  .wrap {
    position: relative;
  }
  .pop {
    position: absolute;
    top: 100%;
    left: 0;
    z-index: 20;
    display: flex;
    flex-direction: column;
    min-width: 180px;
    padding: 4px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    background: var(--tm-surface);
    box-shadow: var(--tm-shadow, 0 8px 24px rgb(0 0 0 / 0.15));
  }
  .pop button {
    border: none;
    background: none;
    text-align: left;
    padding: 7px 10px;
    border-radius: 6px;
    font: inherit;
    font-size: 13px;
    color: var(--tm-fg);
    cursor: pointer;
  }
  .pop button:hover {
    background: var(--tm-hover, color-mix(in srgb, var(--tm-accent) 10%, transparent));
  }
  .crypto {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px;
    margin-left: 4px;
    color: var(--tm-muted);
  }
  .missing {
    color: #dc2626;
    font-size: 12px;
  }
  :global(::highlight(tm-spell)) {
    text-decoration: underline wavy #dc2626;
    text-decoration-thickness: 1.5px;
    text-underline-offset: 3px;
  }
</style>
