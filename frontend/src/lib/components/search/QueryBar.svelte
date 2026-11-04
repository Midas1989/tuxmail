<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Expert query bar (KONZEPT 8.4.1): the query TEXT is the source of truth.
  Chip view: the committed part of the text is rendered as objects
  (field:value chips, operator pills, matching bracket objects); the part
  being edited lives in the input. Backspace on an empty input (or on a
  focused chip) turns the last chip back into editable text.
  Text view: raw input with syntax highlighting underneath.
  ARIA combobox pattern for the context-aware autocomplete.
-->
<script lang="ts">
  import X from '@lucide/svelte/icons/x';
  import Type from '@lucide/svelte/icons/type';
  import Shapes from '@lucide/svelte/icons/shapes';
  import { contextAt, matchParens, tokenize, type CompletionContext, type Token } from '../../search/query';
  import { applySuggestion, fieldCategory, suggestionsFor, type SuggestData, type Suggestion } from '../../search/suggest';
  import { t } from '../../i18n/index.svelte';
  import Avatar from '../common/Avatar.svelte';

  interface Props {
    value: string;
    data: SuggestData;
    error?: { message: string; position: number; code?: string; token?: string } | null;
    onsubmit: () => void;
    placeholder?: string;
  }
  let { value = $bindable(), data, error = null, onsubmit, placeholder = '' }: Props = $props();

  const uid = `qb-${Math.random().toString(36).slice(2, 7)}`;
  let textView = $state(false);
  // A query passed in from outside (saved search folder, project rule) starts as chips.
  // svelte-ignore state_referenced_locally
  let editStart = $state(value.trim() && !tokenize(value).some((tk) => tk.unterminated) ? value.length : 0);
  let input = $state<HTMLInputElement>();
  let focused = $state(false);
  let open = $state(false);
  let active = $state(0);
  let caretTick = $state(0);
  let hoverParen = $state<number | null>(null);
  let chipEls: HTMLElement[] = $state([]);

  // Keep editStart valid when the value changes from outside (facet click, history).
  $effect(() => {
    if (editStart > value.length) editStart = 0;
  });

  const committed = $derived(textView ? '' : value.slice(0, editStart));
  const draft = $derived(textView ? value : value.slice(editStart));
  const offset = $derived(textView ? 0 : editStart);

  // ----- chips -------------------------------------------------------------

  interface Chip {
    kind: 'clause' | 'fieldgroup' | 'term' | 'op' | 'paren' | 'mod';
    start: number;
    end: number;
    text: string;
    field?: string;
    value?: string;
    tokenIndex: number;
    pair?: number;
    unmatched?: boolean;
    category?: string;
  }

  const allTokens = $derived(tokenize(value));
  const parens = $derived(matchParens(allTokens));

  const chips = $derived.by((): Chip[] => {
    const out: Chip[] = [];
    for (let i = 0; i < allTokens.length; i++) {
      const tk = allTokens[i];
      if (tk.end > editStart || textView) break;
      if (tk.type === 'ws') continue;
      if (tk.type === 'field') {
        const next = allTokens[i + 1];
        if (next && next.start === tk.end && next.type !== 'ws' && next.type !== 'lparen' && next.end <= editStart) {
          out.push({ kind: 'clause', start: tk.start, end: next.end, text: tk.text + next.text, field: tk.field, value: next.text, tokenIndex: i, category: fieldCategory(tk.field) });
          i++;
          continue;
        }
        out.push({ kind: 'fieldgroup', start: tk.start, end: tk.end, text: tk.text, field: tk.field, tokenIndex: i, category: fieldCategory(tk.field) });
        continue;
      }
      if (tk.type === 'op') out.push({ kind: 'op', start: tk.start, end: tk.end, text: tk.text, tokenIndex: i });
      else if (tk.type === 'lparen' || tk.type === 'rparen') {
        const pair = parens.match.get(i);
        out.push({ kind: 'paren', start: tk.start, end: tk.end, text: tk.text, tokenIndex: i, pair: pair ?? -1, unmatched: pair === -1 || pair === undefined });
      } else if (tk.type === 'mod') out.push({ kind: 'mod', start: tk.start, end: tk.end, text: tk.text, tokenIndex: i });
      else out.push({ kind: 'term', start: tk.start, end: tk.end, text: tk.text, tokenIndex: i });
    }
    return out;
  });

  function chipHasError(c: Chip): boolean {
    return !!error && error.position >= c.start && error.position < Math.max(c.end, c.start + 1);
  }

  function removeChip(c: Chip) {
    let before = value.slice(0, c.start);
    let after = value.slice(c.end);
    if (/\s$/.test(before) && /^\s/.test(after)) after = after.replace(/^\s+/, '');
    if (!before.trim()) before = '';
    const removed = value.length - (before + after).length;
    value = before + after;
    editStart = Math.max(0, Math.min(editStart - removed, value.length));
    if (editStart > 0 && !/\s$/.test(value.slice(0, editStart))) editStart = 0;
    queueMicrotask(() => input?.focus());
  }

  function editChip(c: Chip) {
    editStart = c.start;
    queueMicrotask(() => {
      input?.focus();
      const end = c.end - c.start;
      input?.setSelectionRange(end, end);
      caretTick++;
      open = true;
    });
  }

  function toggleOperator(c: Chip) {
    const swap: Record<string, string> = { AND: 'OR', OR: 'AND', UND: 'ODER', ODER: 'UND', '&&': '||', '||': '&&' };
    const next = swap[c.text];
    if (!next) return editChip(c);
    value = value.slice(0, c.start) + next + value.slice(c.end);
    editStart += next.length - c.text.length;
  }

  function onChipKey(e: KeyboardEvent, c: Chip, idx: number) {
    if (e.key === 'Backspace' || e.key === 'Delete') {
      e.preventDefault();
      removeChip(c);
    } else if (e.key === 'Enter' || e.key === 'F2') {
      e.preventDefault();
      if (c.kind === 'op') toggleOperator(c);
      else editChip(c);
    } else if (e.key === 'ArrowLeft') {
      e.preventDefault();
      chipEls[Math.max(0, idx - 1)]?.focus();
    } else if (e.key === 'ArrowRight') {
      e.preventDefault();
      if (idx + 1 < chips.length) chipEls[idx + 1]?.focus();
      else input?.focus();
    }
  }

  // ----- autocomplete ---------------------------------------------------------

  function caretPos(): number {
    void caretTick;
    return offset + (input?.selectionStart ?? draft.length);
  }

  const ctx = $derived.by((): CompletionContext => {
    void caretTick;
    void value;
    return contextAt(value, caretPos());
  });
  const suggestions = $derived(focused && open ? suggestionsFor(ctx, data) : []);

  $effect(() => {
    void suggestions.length;
    active = 0;
  });

  function setDraft(d: string) {
    // Committed chips and the new draft are separate terms ("a" + "b" must not become "ab").
    const c = committed;
    const sep = c && d && !/\s$/.test(c) && !/^\s/.test(d) ? ' ' : '';
    value = c + sep + d;
    if (sep) editStart = c.length + 1;
  }

  function maybeCommit() {
    if (textView) return;
    const d = draft;
    if (!/\s$/.test(d) || !d.trim()) return;
    const toks = tokenize(value);
    if (toks.some((tk) => tk.unterminated)) return;
    // only commit when the caret is at the end
    if ((input?.selectionStart ?? 0) < d.length) return;
    editStart = value.length;
  }

  function accept(s: Suggestion) {
    const r = applySuggestion(value, ctx, s);
    value = r.text;
    if (!textView && s.complete && r.caret >= value.length) editStart = value.length;
    queueMicrotask(() => {
      if (!input) return;
      input.focus();
      const pos = Math.max(0, r.caret - offset);
      input.setSelectionRange(pos, pos);
      caretTick++;
      open = true;
    });
  }

  function onkeydown(e: KeyboardEvent) {
    const list = suggestions;
    if (list.length && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault();
      active = (active + (e.key === 'ArrowDown' ? 1 : -1) + list.length) % list.length;
      return;
    }
    if (list.length && (e.key === 'Tab' || (e.key === 'Enter' && open))) {
      e.preventDefault();
      accept(list[active]);
      return;
    }
    if (e.key === 'Enter') {
      e.preventDefault();
      open = false;
      onsubmit();
      return;
    }
    if (e.key === 'Escape') {
      if (open && list.length) {
        e.preventDefault();
        e.stopPropagation();
        open = false;
      }
      return;
    }
    if (!textView && e.key === 'Backspace' && input && input.selectionStart === 0 && input.selectionEnd === 0 && chips.length) {
      e.preventDefault();
      const last = chips[chips.length - 1];
      editStart = last.start;
      queueMicrotask(() => {
        const end = last.end - last.start;
        input?.setSelectionRange(end, end);
        caretTick++;
      });
      return;
    }
    if (!textView && e.key === 'ArrowLeft' && input?.selectionStart === 0 && chips.length) {
      e.preventDefault();
      chipEls[chips.length - 1]?.focus();
    }
  }

  // ----- syntax highlighting for the text view ----------------------------------

  function cls(tk: Token): string {
    if (tk.type === 'field') return `hl-field cat-${fieldCategory(tk.field) ?? 'text'}`;
    if (tk.type === 'op') return 'hl-op';
    if (tk.type === 'lparen' || tk.type === 'rparen') return 'hl-paren';
    if (tk.type === 'phrase') return 'hl-phrase';
    if (tk.type === 'range' || tk.type === 'regex') return 'hl-range';
    if (tk.type === 'mod') return 'hl-op';
    if (tk.field) return 'hl-value';
    return 'hl-term';
  }

  let hlEl = $state<HTMLElement>();
  function syncScroll() {
    if (hlEl && input) hlEl.scrollLeft = input.scrollLeft;
  }

  const errorText = $derived(
    error ? (error.code ? t(`search.error.${error.code}` as 'search.error.unexpectedEnd', { token: error.token ?? '' }) : error.message) : '',
  );

  const opLabel = (s: Suggestion) => (s.descKey ? t(s.descKey as 'search.op.and') : (s.detail ?? ''));
</script>

<div class="qb" class:focused class:has-error={!!error}>
  <div class="field-row">
    <div class="chips" class:raw={textView} role="group" aria-label={t('search.chipsLabel')}>
    {#if !textView}
        {#each chips as c, i (c.start + ':' + c.text)}
          {@const pairActive = hoverParen != null && (hoverParen === c.tokenIndex || hoverParen === c.pair)}
          <span

            class="chip chip-{c.kind} {c.category ? 'cat-' + c.category : ''}"
            class:paired={c.kind === 'paren' && pairActive}
            class:bad={(c.kind === 'paren' && c.unmatched) || chipHasError(c)}
          >
            <button
              bind:this={chipEls[i]}
              class="chip-main"
              onclick={() => (c.kind === 'op' ? toggleOperator(c) : editChip(c))}
              onkeydown={(e) => onChipKey(e, c, i)}
              onmouseenter={() => c.kind === 'paren' && (hoverParen = c.tokenIndex)}
              onmouseleave={() => (hoverParen = null)}
              onfocus={() => c.kind === 'paren' && (hoverParen = c.tokenIndex)}
              onblur={() => (hoverParen = null)}
              title={c.kind === 'op' ? t('search.toggleOperator') : c.kind === 'paren' && c.unmatched ? t('search.unmatchedParen') : t('search.editChip')}
              aria-label={c.kind === 'clause' ? t('search.chipClause', { field: c.field ?? '', value: c.value ?? '' }) : c.text}
            >
              {#if c.kind === 'clause'}
                <span class="f">{c.field}</span><span class="v">{c.value}</span>
              {:else}
                {c.text}
              {/if}
            </button>
            {#if c.kind === 'clause' || c.kind === 'term'}
              <button class="chip-x" tabindex="-1" aria-label={t('search.removeChip', { text: c.text })} onclick={() => removeChip(c)}><X size={11} /></button>
            {/if}
          </span>
        {/each}
    {/if}
    <div class="inwrap" class:raw={textView}>
      {#if textView}
        <div class="hl" bind:this={hlEl} aria-hidden="true">
          {#each allTokens as tk, i (i)}{@const pi = parens.match.get(i)}<span class="{cls(tk)}" class:bad-paren={(tk.type === 'lparen' || tk.type === 'rparen') && pi === -1} class:err={!!error && error.position >= tk.start && error.position < Math.max(tk.end, tk.start + 1)}>{tk.text}</span>{/each}{#if error && error.position >= value.length}<span class="err-end">&nbsp;</span>{/if}
        </div>
      {/if}
      <input
        bind:this={input}
        class="q-input"
        value={draft}
        oninput={(e) => {
          setDraft((e.currentTarget as HTMLInputElement).value);
          open = true;
          caretTick++;
          maybeCommit();
          syncScroll();
        }}
        onkeydown={onkeydown}
        onkeyup={(e) => {
          if (['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) caretTick++;
        }}
        onclick={() => {
          caretTick++;
          open = true;
        }}
        onscroll={syncScroll}
        onfocus={() => {
          focused = true;
          open = true;
          caretTick++;
        }}
        onblur={() => setTimeout(() => (focused = false), 120)}
        role="combobox"
        aria-expanded={suggestions.length > 0}
        aria-controls="{uid}-list"
        aria-autocomplete="list"
        aria-activedescendant={suggestions.length ? `${uid}-opt-${active}` : undefined}
        aria-label={t('search.expertLabel')}
        aria-invalid={!!error}
        aria-describedby={error ? `${uid}-err` : undefined}
        placeholder={chips.length ? '' : placeholder}
        spellcheck="false"
        autocomplete="off"
        autocapitalize="off"
        data-search-input
      />
    </div>
    </div>
    <button
      class="icon-btn view-toggle"
      aria-pressed={textView}
      title={textView ? t('search.chipView') : t('search.textView')}
      aria-label={textView ? t('search.chipView') : t('search.textView')}
      onclick={() => {
        textView = !textView;
        editStart = textView ? 0 : value.length;
        queueMicrotask(() => input?.focus());
      }}
    >
      {#if textView}<Shapes size={16} />{:else}<Type size={16} />{/if}
    </button>
  </div>

  {#if suggestions.length}
    <ul class="sugg" id="{uid}-list" role="listbox" aria-label={t('search.suggestions')}>
      {#each suggestions as s, i (s.id)}
        <li
          id="{uid}-opt-{i}"
          role="option"
          aria-selected={i === active}
          class="s-{s.kind} {s.category ? 'cat-' + s.category : ''}"
          onmousedown={(e) => {
            e.preventDefault();
            accept(s);
          }}
          onmousemove={() => (active = i)}
        >
          {#if s.avatar}
            <Avatar name={s.avatar.name} seed={s.avatar.seed} size={22} />
          {:else}
            <span class="s-icon">{s.kind === 'field' ? ':' : s.kind === 'operator' ? '⊕' : s.kind === 'paren' ? s.label : '•'}</span>
          {/if}
          <span class="s-label">{s.label}</span>
          <span class="s-detail">{opLabel(s)}</span>
          {#if s.detail && s.descKey}<span class="s-detail">{s.detail}</span>{/if}
        </li>
      {/each}
    </ul>
  {/if}

  {#if error}
    <p class="error" id="{uid}-err" role="alert">
      {t('search.errorAt', { message: errorText, position: error.position + 1 })}
      {#if textView}<code class="caret-line">{value.slice(0, error.position).replace(/./g, ' ')}^</code>{/if}
    </p>
  {/if}
</div>

<style>
  .qb {
    position: relative;
    flex: 1;
    min-width: 0;
  }
  .field-row {
    display: flex;
    align-items: center;
    gap: 6px;
    min-height: 42px;
    padding: 4px 4px 4px 8px;
    border-radius: 12px;
    border: 1px solid var(--tm-line-strong);
    background: var(--tm-surface);
    font-family: var(--tm-mono);
    font-size: 13.5px;
    transition:
      border-color var(--tm-dur),
      box-shadow var(--tm-dur);
  }
  .qb.focused .field-row {
    border-color: var(--tm-accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--tm-accent) 18%, transparent);
  }
  .qb.has-error .field-row {
    border-color: var(--tm-danger);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
    flex: 1;
    min-width: 0;
  }
  .chip {
    --c: var(--tm-muted);
    display: inline-flex;
    align-items: center;
    height: 26px;
    border-radius: 7px;
    background: color-mix(in srgb, var(--c) 14%, var(--tm-surface));
    border: 1px solid color-mix(in srgb, var(--c) 35%, transparent);
    color: var(--tm-fg);
    overflow: hidden;
  }
  .chip-main {
    display: inline-flex;
    align-items: center;
    gap: 1px;
    height: 100%;
    padding: 0 7px;
    border: none;
    background: transparent;
    color: inherit;
    font: inherit;
    cursor: pointer;
  }
  .chip-main:focus-visible {
    outline: 2px solid var(--tm-focus);
    outline-offset: -2px;
  }
  .chip .f {
    color: color-mix(in srgb, var(--c) 80%, var(--tm-fg));
    font-weight: 600;
  }
  .chip .f::after {
    content: ':';
  }
  .chip-x {
    display: inline-flex;
    align-items: center;
    height: 100%;
    padding: 0 5px 0 2px;
    border: none;
    background: transparent;
    color: var(--tm-muted);
  }
  .chip-x:hover {
    color: var(--tm-danger);
  }
  .cat-person {
    --c: #2563eb;
  }
  .cat-text {
    --c: #64748b;
  }
  .cat-place {
    --c: #9333ea;
  }
  .cat-time {
    --c: #0891b2;
  }
  .cat-state {
    --c: #16a34a;
  }
  .cat-size {
    --c: #d97706;
  }
  .cat-type {
    --c: #db2777;
  }
  .chip-term {
    --c: var(--tm-faint);
  }
  .chip-op {
    --c: var(--tm-brand, #f59e0b);
    border-radius: 99px;
    font-weight: 700;
    font-size: 11.5px;
    letter-spacing: 0.04em;
  }
  .chip-mod {
    --c: var(--tm-danger);
  }
  .chip-paren {
    --c: var(--tm-faint);
    background: transparent;
    font-weight: 700;
    font-size: 16px;
    border-color: transparent;
  }
  .chip-paren.paired {
    background: color-mix(in srgb, var(--tm-accent) 18%, transparent);
    border-color: var(--tm-accent);
  }
  .chip.bad {
    --c: var(--tm-danger);
    border-color: var(--tm-danger);
    color: var(--tm-danger);
  }
  .inwrap {
    position: relative;
    flex: 1;
    min-width: 120px;
    height: 30px;
  }
  .q-input {
    position: absolute;
    inset: 0;
    width: 100%;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    outline: none;
    padding: 0 2px;
  }
  .raw .q-input {
    color: transparent;
    caret-color: var(--tm-fg);
  }
  .hl {
    position: absolute;
    inset: 0;
    padding: 0 2px;
    white-space: pre;
    overflow: hidden;
    line-height: 30px;
    pointer-events: none;
  }
  .hl-field {
    color: color-mix(in srgb, var(--c) 85%, var(--tm-fg));
    font-weight: 600;
  }
  .hl-value {
    color: var(--tm-fg);
    background: color-mix(in srgb, var(--tm-accent) 8%, transparent);
    border-radius: 3px;
  }
  .hl-op {
    color: #d97706;
    font-weight: 700;
  }
  .hl-paren {
    color: var(--tm-accent-text);
    font-weight: 700;
  }
  .hl-phrase {
    color: #15803d;
  }
  .hl-range {
    color: #0891b2;
  }
  .hl-term {
    color: var(--tm-fg);
  }
  .bad-paren,
  .err {
    color: var(--tm-danger) !important;
    text-decoration: wavy underline var(--tm-danger);
  }
  .err-end {
    border-bottom: 2px solid var(--tm-danger);
  }
  .view-toggle {
    flex: none;
  }
  .sugg {
    position: absolute;
    left: 0;
    right: 0;
    top: calc(100% + 4px);
    z-index: 30;
    list-style: none;
    margin: 0;
    padding: 4px;
    border-radius: 12px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
    max-height: 320px;
    overflow: auto;
  }
  .sugg li {
    --c: var(--tm-muted);
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 10px;
    border-radius: 8px;
    cursor: pointer;
    font-size: 13px;
  }
  .sugg li[aria-selected='true'] {
    background: var(--tm-accent-soft);
  }
  .s-icon {
    width: 22px;
    height: 22px;
    flex: none;
    border-radius: 6px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-family: var(--tm-mono);
    font-weight: 700;
    color: color-mix(in srgb, var(--c) 85%, var(--tm-fg));
    background: color-mix(in srgb, var(--c) 15%, var(--tm-surface));
  }
  .s-operator .s-icon {
    --c: #d97706;
  }
  .s-label {
    font-family: var(--tm-mono);
    font-weight: 600;
  }
  .s-detail {
    color: var(--tm-muted);
    font-size: 12px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .error {
    margin: 6px 2px 0;
    color: var(--tm-danger);
    font-size: 12.5px;
  }
  .caret-line {
    display: block;
    white-space: pre;
    font-family: var(--tm-mono);
    font-size: 13.5px;
    padding-left: 11px;
    overflow: hidden;
  }
</style>
