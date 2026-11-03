<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Source editor for workflows. A transparent textarea lies exactly over a
  rendered copy of the text, so the copy can show syntax colours, rule-syntax
  chips (keyword/trigger/field/action chips like the search expert mode) and
  diagnostics (wavy underlines + gutter markers) while editing stays native
  (IME, undo, screen readers). Completion comes from Api.completeWorkflow.
-->
<script lang="ts">
  import CircleAlert from '@lucide/svelte/icons/circle-alert';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import type { WorkflowCompletion, WorkflowCompletionItem, WorkflowDiagnostic, WorkflowSchema } from '../../types';
  import { parseRule } from '../../workflow/rule';
  import { highlightYaml, offsetAt } from '../../workflow/lang';
  import { EMPTY_CONTEXT } from '../../workflow/schema';
  import { i18n, t, type MessageKey } from '../../i18n/index.svelte';

  interface Props {
    value: string;
    mode: 'rule' | 'yaml';
    schema: WorkflowSchema;
    diagnostics: WorkflowDiagnostic[];
    complete: (src: string, offset: number) => Promise<WorkflowCompletion>;
    label: string;
    placeholder?: string;
  }
  let { value = $bindable(), mode, schema, diagnostics, complete, label, placeholder = '' }: Props = $props();

  const uid = 'ce-' + Math.random().toString(36).slice(2, 7);
  let ta = $state<HTMLTextAreaElement>();
  let caretEl = $state<HTMLSpanElement>();
  let caret = $state(0);
  let comp = $state<WorkflowCompletion | null>(null);
  let active = $state(0);
  let popPos = $state({ x: 0, y: 0 });
  let focused = $state(false);

  // ----- per-character classes ----------------------------------------------------
  const classes = $derived.by((): string[] => {
    const cls: string[] = new Array(value.length).fill('');
    if (mode === 'rule') {
      const r = parseRule(value, schema, EMPTY_CONTEXT);
      for (const tk of r.tokens) {
        if (tk.type === 'ws') continue;
        let c = tk.role ? 'c-' + tk.role : '';
        if (tk.type === 'string') c += ' c-str';
        for (let i = tk.start; i < tk.end; i++) cls[i] = c;
      }
    } else {
      let off = 0;
      for (const line of highlightYaml(value)) {
        for (const seg of line) {
          for (let i = 0; i < seg.text.length; i++) cls[off + i] = seg.cls ? 'y-' + seg.cls : '';
          off += seg.text.length;
        }
        off++; // newline
      }
    }
    return cls;
  });

  const flags = $derived.by((): string[] => {
    const f: string[] = new Array(value.length).fill('');
    for (const d of diagnostics) {
      let a = offsetAt(value, d.line, d.col);
      let b = offsetAt(value, d.endLine, d.endCol);
      a = Math.max(0, Math.min(a, value.length - 1));
      b = Math.max(a + 1, Math.min(b, value.length));
      for (let i = a; i < b; i++) if (f[i] !== 'e') f[i] = d.severity === 'error' ? 'e' : 'w';
    }
    return f;
  });

  interface Run {
    text: string;
    cls: string;
    caret?: boolean;
  }
  const runs = $derived.by((): Run[] => {
    const out: Run[] = [];
    let cur: Run | null = null;
    for (let i = 0; i <= value.length; i++) {
      if (i === caret) {
        out.push({ text: '', cls: '', caret: true });
        cur = null;
      }
      if (i === value.length) break;
      const ch = value[i];
      const c = (ch === '\n' ? '' : classes[i]) + (flags[i] && ch !== '\n' ? ' sq-' + flags[i] : '');
      if (cur && cur.cls === c) cur.text += ch;
      else {
        cur = { text: ch, cls: c };
        out.push(cur);
      }
    }
    return out;
  });

  const lines = $derived(value.split('\n'));
  const lineDiag = $derived.by(() => {
    const m = new Map<number, WorkflowDiagnostic>();
    for (const d of diagnostics) {
      const prev = m.get(d.line);
      if (!prev || (prev.severity !== 'error' && d.severity === 'error')) m.set(d.line, d);
    }
    return m;
  });

  export function diagText(d: WorkflowDiagnostic): string {
    const k = 'workflow.diag.' + d.code;
    return i18n.has(k) ? t(k as MessageKey, { detail: d.detail ?? '' }) : d.message;
  }

  // ----- completion -----------------------------------------------------------------
  let timer: ReturnType<typeof setTimeout> | null = null;
  let seq = 0;
  function syncCaret() {
    if (ta) caret = ta.selectionStart ?? 0;
  }
  function requestCompletion(force = false) {
    if (timer) clearTimeout(timer);
    timer = setTimeout(async () => {
      if (!ta) return;
      const off = ta.selectionStart ?? 0;
      const before = value.slice(0, off);
      if (!force && !/[\p{L}\p{N}_.:{=,\s-]$/u.test(before)) {
        comp = null;
        return;
      }
      const my = ++seq;
      const r = await complete(value, off);
      if (my !== seq || !focused) return;
      comp = r.items.length ? r : null;
      active = 0;
      requestAnimationFrame(placePopup);
    }, force ? 0 : 140);
  }
  function placePopup() {
    if (!caretEl) return;
    const r = caretEl.getBoundingClientRect();
    popPos = { x: Math.min(r.left, window.innerWidth - 390), y: r.bottom + 4 };
  }

  function accept(it: WorkflowCompletionItem) {
    if (!comp || !ta) return;
    const { from, to } = comp;
    value = value.slice(0, from) + it.insert + value.slice(to);
    const pos = from + it.insert.length;
    comp = null;
    queueMicrotask(() => {
      ta?.focus();
      ta?.setSelectionRange(pos, pos);
      syncCaret();
      if (/[:=\s]$/.test(it.insert)) requestCompletion(true);
    });
  }

  function onkeydown(e: KeyboardEvent) {
    if (comp) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        active = (active + (e.key === 'ArrowDown' ? 1 : -1) + comp.items.length) % comp.items.length;
        return;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        accept(comp.items[active]);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        comp = null;
        return;
      }
    }
    if (e.key === ' ' && e.ctrlKey) {
      e.preventDefault();
      requestCompletion(true);
      return;
    }
    if (mode === 'yaml' && e.key === 'Tab' && !e.shiftKey) {
      e.preventDefault();
      const s = ta!.selectionStart;
      value = value.slice(0, s) + '  ' + value.slice(ta!.selectionEnd);
      queueMicrotask(() => ta?.setSelectionRange(s + 2, s + 2));
    }
    if (mode === 'yaml' && e.key === 'Enter' && !e.shiftKey) {
      // keep the indentation (+2 after "key:" or "- ")
      e.preventDefault();
      const s = ta!.selectionStart;
      const lineStart = value.lastIndexOf('\n', s - 1) + 1;
      const line = value.slice(lineStart, s);
      let ind = (/^\s*/.exec(line) ?? [''])[0];
      if (/:\s*$/.test(line) || /^\s*- /.test(line)) ind += '  ';
      const ins = '\n' + ind;
      value = value.slice(0, s) + ins + value.slice(ta!.selectionEnd);
      queueMicrotask(() => {
        ta?.setSelectionRange(s + ins.length, s + ins.length);
        syncCaret();
      });
    }
  }

  /** Move the caret to a diagnostic (diagnostics list). */
  export function reveal(d: WorkflowDiagnostic) {
    const off = offsetAt(value, d.line, d.col);
    ta?.focus();
    ta?.setSelectionRange(off, offsetAt(value, d.endLine, d.endCol));
    syncCaret();
  }
</script>

<div class="ce" class:rule={mode === 'rule'} class:yaml={mode === 'yaml'}>
  <div class="scroll">
    {#if mode === 'yaml'}
      <div class="gutter" aria-hidden="true">
        {#each lines as _, i (i)}
          {@const d = lineDiag.get(i + 1)}
          <div class="ln" class:err={d?.severity === 'error'} class:warn={d && d.severity !== 'error'} title={d ? diagText(d) : ''}>
            {#if d}<span class="mark">{#if d.severity === 'error'}<CircleAlert size={11} />{:else}<TriangleAlert size={11} />{/if}</span>{/if}{i + 1}
          </div>
        {/each}
      </div>
    {/if}
    <div class="code">
      <pre class="hl" aria-hidden="true">{#each runs as r, i (i)}{#if r.caret}<span class="caret-mark" bind:this={caretEl}></span>{:else}<span class={r.cls}>{r.text}</span>{/if}{/each}{'\n '}</pre>
      <textarea
        bind:this={ta}
        bind:value
        class="input-layer"
        spellcheck="false"
        autocomplete="off"
        autocapitalize="off"
        wrap={mode === 'yaml' ? 'off' : 'soft'}
        aria-label={label}
        {placeholder}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={!!comp}
        aria-controls={uid + '-list'}
        aria-activedescendant={comp ? `${uid}-opt-${active}` : undefined}
        aria-invalid={diagnostics.some((d) => d.severity === 'error')}
        oninput={() => {
          syncCaret();
          requestCompletion();
        }}
        onclick={() => {
          syncCaret();
          comp = null;
        }}
        onkeyup={(e) => {
          if (e.key.startsWith('Arrow') || e.key === 'Home' || e.key === 'End') syncCaret();
        }}
        onfocus={() => (focused = true)}
        onblur={() => {
          focused = false;
          setTimeout(() => (comp = null), 150);
        }}
        {onkeydown}
      ></textarea>
    </div>
  </div>
  {#if comp}
    <ul class="pop" id={uid + '-list'} role="listbox" aria-label={t('workflow.editor.suggestions')} style:left="{popPos.x}px" style:top="{popPos.y}px">
      {#each comp.items as it, i (it.kind + it.label + i)}
        <li id="{uid}-opt-{i}" role="option" aria-selected={i === active} class:active={i === active}>
          <button tabindex="-1" onmousedown={(e) => (e.preventDefault(), accept(it))}>
            <span class="kind k-{it.kind}">{t(`workflow.completion.${it.kind}`)}</span>
            <span class="lbl">{it.label}</span>
            {#if it.detail}<span class="det">{it.detail}</span>{/if}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .ce {
    --lh: 21px;
    position: relative;
    border: 1px solid var(--tm-line-strong);
    border-radius: 10px;
    background: var(--tm-surface);
  }
  .ce:focus-within {
    border-color: var(--tm-accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-accent) 18%, transparent);
  }
  .scroll {
    display: flex;
    align-items: flex-start;
    overflow: auto;
    max-height: min(420px, 48vh);
    min-height: 64px;
  }
  .yaml .scroll {
    min-height: 260px;
    /* gutter background for the whole height, also below the last line */
    background: linear-gradient(90deg, var(--tm-surface-2) 52px, var(--tm-line) 52px, var(--tm-line) 53px, transparent 53px);
  }
  .gutter {
    flex: none;
    width: 53px;
    box-sizing: border-box;
    padding: 10px 0;
    background: var(--tm-surface-2);
    border-right: 1px solid var(--tm-line);
    font-family: var(--tm-mono);
    font-size: 12px;
    color: var(--tm-faint);
    text-align: right;
    user-select: none;
    position: sticky;
    left: 0;
    z-index: 2;
  }
  .ln {
    height: var(--lh);
    line-height: var(--lh);
    padding: 0 8px 0 20px;
    position: relative;
  }
  .ln .mark {
    position: absolute;
    left: 4px;
    top: 50%;
    transform: translateY(-50%);
    display: inline-flex;
  }
  .ln.err {
    color: var(--tm-danger);
    background: color-mix(in srgb, var(--tm-danger) 10%, transparent);
  }
  .ln.warn {
    color: var(--tm-warn);
  }
  .code {
    position: relative;
    flex: 1;
    min-width: 0;
    min-height: 100%;
  }
  .yaml .code {
    min-width: max-content;
  }
  .hl,
  .input-layer {
    margin: 0;
    padding: 10px 12px;
    font-family: var(--tm-mono);
    font-size: 13px;
    line-height: var(--lh);
    letter-spacing: 0;
    tab-size: 2;
    box-sizing: border-box;
  }
  .rule .hl,
  .rule .input-layer {
    white-space: pre-wrap;
    word-break: break-word;
    overflow-wrap: anywhere;
    padding: 12px 14px;
    line-height: 26px;
  }
  .yaml .hl,
  .yaml .input-layer {
    white-space: pre;
  }
  .hl {
    color: var(--tm-fg);
    pointer-events: none;
    min-height: 100%;
  }
  .input-layer {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    resize: none;
    border: none;
    outline: none;
    overflow: hidden;
    background: transparent;
    color: transparent;
    caret-color: var(--tm-fg);
  }
  .input-layer::selection {
    background: color-mix(in srgb, var(--tm-accent) 30%, transparent);
    color: transparent;
  }
  .input-layer::placeholder {
    color: var(--tm-faint);
  }
  .caret-mark {
    display: inline-block;
    width: 0;
    height: 1em;
  }
  /* --- rule syntax chips (box-shadow keeps the text metrics identical) --- */
  .hl :global(span[class*='c-']) {
    border-radius: 4px;
  }
  .hl :global(.c-kw) {
    background: var(--tm-brand-navy);
    box-shadow: 0 0 0 3px var(--tm-brand-navy);
    color: #fff;
    font-weight: 700;
  }
  .hl :global(.c-trigger) {
    background: color-mix(in srgb, var(--tm-brand) 22%, var(--tm-surface));
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-brand) 22%, var(--tm-surface));
    color: color-mix(in srgb, var(--tm-brand) 55%, var(--tm-fg));
    font-weight: 650;
  }
  .hl :global(.c-scope),
  .hl :global(.c-field) {
    background: color-mix(in srgb, #7c3aed 13%, var(--tm-surface));
    box-shadow: 0 0 0 3px color-mix(in srgb, #7c3aed 13%, var(--tm-surface));
    color: color-mix(in srgb, #7c3aed 70%, var(--tm-fg));
    font-weight: 600;
  }
  .hl :global(.c-op) {
    background: color-mix(in srgb, #7c3aed 13%, var(--tm-surface));
    box-shadow: 0 0 0 3px color-mix(in srgb, #7c3aed 13%, var(--tm-surface));
    color: var(--tm-muted);
  }
  .hl :global(.c-value) {
    background: color-mix(in srgb, var(--tm-accent) 11%, var(--tm-surface));
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-accent) 11%, var(--tm-surface));
    color: var(--tm-accent-text);
  }
  .hl :global(.c-action) {
    background: color-mix(in srgb, var(--tm-success) 18%, var(--tm-surface));
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-success) 18%, var(--tm-surface));
    color: color-mix(in srgb, var(--tm-success) 70%, var(--tm-fg));
    font-weight: 700;
  }
  .hl :global(.c-param) {
    color: var(--tm-muted);
    font-style: italic;
  }
  .hl :global(.c-punct) {
    color: var(--tm-muted);
  }
  .hl :global(.c-error) {
    color: var(--tm-danger);
  }
  /* --- YAML --- */
  .hl :global(.y-key) {
    color: color-mix(in srgb, #7c3aed 75%, var(--tm-fg));
  }
  .hl :global(.y-kw) {
    color: color-mix(in srgb, var(--tm-brand) 60%, var(--tm-fg));
    font-weight: 650;
  }
  .hl :global(.y-str) {
    color: color-mix(in srgb, var(--tm-success) 75%, var(--tm-fg));
  }
  .hl :global(.y-num),
  .hl :global(.y-bool) {
    color: color-mix(in srgb, #0ea5e9 80%, var(--tm-fg));
  }
  .hl :global(.y-comment) {
    color: var(--tm-faint);
    font-style: italic;
  }
  .hl :global(.y-var) {
    color: color-mix(in srgb, var(--tm-brand) 70%, var(--tm-fg));
    font-weight: 650;
  }
  .hl :global(.y-punct) {
    color: var(--tm-muted);
  }
  /* --- diagnostics --- */
  .hl :global(.sq-e) {
    text-decoration: underline wavy var(--tm-danger);
    text-decoration-skip-ink: none;
    text-underline-offset: 3px;
  }
  .hl :global(.sq-w) {
    text-decoration: underline wavy var(--tm-warn);
    text-decoration-skip-ink: none;
    text-underline-offset: 3px;
  }
  /* --- completion popup --- */
  .pop {
    position: fixed;
    z-index: 20;
    min-width: 240px;
    max-width: 380px;
    max-height: 240px;
    overflow-y: auto;
    margin: 0;
    padding: 4px;
    list-style: none;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line-strong);
    border-radius: 10px;
    box-shadow: var(--tm-shadow-lg);
    font-size: 13px;
  }
  .pop button {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 5px 8px;
    border: none;
    border-radius: 7px;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .pop li.active button {
    background: var(--tm-accent-soft);
  }
  .kind {
    flex: none;
    width: 62px;
    font-size: 10px;
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--tm-faint);
  }
  .k-keyword {
    color: var(--tm-brand-navy);
  }
  .k-trigger {
    color: var(--tm-brand);
  }
  .k-action {
    color: var(--tm-success);
  }
  .k-field,
  .k-scope {
    color: #7c3aed;
  }
  .lbl {
    font-family: var(--tm-mono);
    font-size: 12.5px;
  }
  .det {
    margin-left: auto;
    font-size: 11px;
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  :global([data-theme='dark']) .hl :global(.c-kw) {
    background: #3b5480;
    box-shadow: 0 0 0 3px #3b5480;
  }
</style>
