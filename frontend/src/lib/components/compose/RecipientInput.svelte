<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts" module>
  const EMAIL_RE = /^[^\s@<>()",;]+@[^\s@<>()",;]+\.[^\s@<>()",;]{2,}$/;

  export function isValidEmail(s: string): boolean {
    return EMAIL_RE.test(s.trim());
  }

  /** Parse "Name <a@b.de>, c@d.de; …" into addresses. */
  export function parseAddresses(input: string): { name?: string; email: string }[] {
    return input
      .split(/[,;\n]+/)
      .map((p) => p.trim())
      .filter(Boolean)
      .map((p) => {
        const m = /^(.*?)\s*<([^>]+)>$/.exec(p);
        if (m) return { name: m[1].replace(/^"|"$/g, '').trim() || undefined, email: m[2].trim() };
        return { email: p };
      });
  }
</script>

<script lang="ts">
  import X from '@lucide/svelte/icons/x';
  import type { Address, AddressSuggestion, Person } from '../../types';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import Avatar from '../common/Avatar.svelte';

  interface Props {
    label: string;
    value: Address[];
    persons: Person[];
    autofocus?: boolean;
  }
  let { label, value = $bindable(), persons, autofocus = false }: Props = $props();

  let query = $state('');
  let active = $state(0);
  let input = $state<HTMLInputElement>();
  let focused = $state(false);
  const listId = `rcpt-${Math.random().toString(36).slice(2, 8)}`;

  // Contacts and recently used addresses from the core (debounced).
  let remote = $state<AddressSuggestion[]>([]);
  let remoteTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    const q = query.trim();
    clearTimeout(remoteTimer);
    if (q.length < 2) {
      remote = [];
      return;
    }
    remoteTimer = setTimeout(() => {
      void app.api
        .autocompleteAddresses(q, 8)
        .then((r) => query.trim() === q && (remote = r))
        .catch(() => (remote = []));
    }, 120);
  });

  const suggestions = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (q.length < 1) return [];
    const out: { name: string; email: string; person?: Person }[] = [];
    for (const p of persons) {
      if (p.isMe) continue;
      for (const h of p.handles) {
        if (h.kind !== 'email') continue;
        if (value.some((v) => v.email === h.value)) continue;
        if (p.displayName.toLowerCase().includes(q) || h.value.includes(q)) out.push({ name: p.displayName, email: h.value, person: p });
      }
    }
    for (const r of remote) {
      if (out.some((o) => o.email.toLowerCase() === r.email.toLowerCase()) || value.some((v) => v.email.toLowerCase() === r.email.toLowerCase())) continue;
      out.push({ name: r.name || r.email, email: r.email });
    }
    return out.slice(0, 8);
  });

  $effect(() => {
    void suggestions.length;
    active = 0;
  });

  function add(addr: Address) {
    if (!addr.email || value.some((v) => v.email.toLowerCase() === addr.email.toLowerCase())) return;
    value = [...value, addr];
  }

  function commit(): boolean {
    const q = query.trim();
    if (!q) return false;
    for (const a of parseAddresses(q)) {
      const known = persons.find((p) => p.handles.some((h) => h.kind === 'email' && h.value === a.email.toLowerCase()));
      add({ name: a.name ?? known?.displayName, email: a.email });
    }
    query = '';
    return true;
  }

  function remove(i: number) {
    value = value.filter((_, j) => j !== i);
    input?.focus();
  }

  function onkeydown(e: KeyboardEvent) {
    if (suggestions.length && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault();
      active = (active + (e.key === 'ArrowDown' ? 1 : -1) + suggestions.length) % suggestions.length;
      return;
    }
    if (e.key === 'Enter' || e.key === 'Tab' || e.key === ',' || e.key === ';') {
      if (suggestions.length && (e.key === 'Enter' || e.key === 'Tab')) {
        e.preventDefault();
        const s = suggestions[active];
        add({ name: s.name, email: s.email });
        query = '';
        return;
      }
      if (query.trim()) {
        e.preventDefault();
        commit();
      }
      return;
    }
    if (e.key === 'Backspace' && !query && value.length) {
      value = value.slice(0, -1);
    }
  }

  function onpaste(e: ClipboardEvent) {
    const txt = e.clipboardData?.getData('text') ?? '';
    if (/[,;\n]/.test(txt)) {
      e.preventDefault();
      query = txt;
      commit();
    }
  }
</script>

<div class="rcpt" class:focused>
  <label class="lbl" for={listId + '-in'}>{label}</label>
  <div class="chips">
    {#each value as a, i (a.email)}
      <span class="chip" class:invalid={!isValidEmail(a.email)} title={a.email}>
        <span>{a.name || a.email}</span>
        <button type="button" aria-label={t('compose.removeRecipient', { name: a.name || a.email })} onclick={() => remove(i)}><X size={12} /></button>
      </span>
    {/each}
    <div class="inwrap">
      <input
        bind:this={input}
        id={listId + '-in'}
        bind:value={query}
        {onkeydown}
        {onpaste}
        onfocus={() => (focused = true)}
        onblur={() => {
          focused = false;
          commit();
        }}
        role="combobox"
        aria-expanded={suggestions.length > 0}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={suggestions.length ? `${listId}-${active}` : undefined}
        autocomplete="off"
        data-autofocus={autofocus ? '' : undefined}
      />
      {#if suggestions.length && focused}
        <ul class="sugg" id={listId} role="listbox">
          {#each suggestions as s, i (s.email)}
            <li
              id="{listId}-{i}"
              role="option"
              aria-selected={i === active}
              onmousedown={(e) => {
                e.preventDefault();
                add({ name: s.name, email: s.email });
                query = '';
              }}
            >
              <Avatar name={s.name} seed={s.email} size={26} />
              <span class="sn">{s.name}</span>
              <span class="se">{s.email}</span>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  </div>
</div>

<style>
  .rcpt {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 4px 0;
    border-bottom: 1px solid var(--tm-line);
  }
  .lbl {
    width: 44px;
    flex: none;
    font-size: 13px;
    color: var(--tm-muted);
    padding-top: 6px;
  }
  .chips {
    flex: 1;
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
    min-width: 0;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    height: 26px;
    padding: 0 4px 0 10px;
    border-radius: 99px;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
    font-size: 13px;
    max-width: 260px;
  }
  .chip span {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .chip.invalid {
    background: color-mix(in srgb, var(--tm-danger) 15%, var(--tm-surface));
    color: var(--tm-danger);
  }
  .chip button {
    display: inline-flex;
    border: none;
    background: transparent;
    color: inherit;
    padding: 3px;
    border-radius: 99px;
  }
  .chip button:hover {
    background: color-mix(in srgb, currentColor 15%, transparent);
  }
  .inwrap {
    position: relative;
    flex: 1;
    min-width: 140px;
  }
  input {
    width: 100%;
    height: 30px;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    outline: none;
  }
  .sugg {
    position: absolute;
    left: 0;
    top: 32px;
    z-index: 5;
    min-width: 300px;
    list-style: none;
    margin: 0;
    padding: 4px;
    border-radius: 10px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
  }
  .sugg li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 5px 8px;
    border-radius: 6px;
    cursor: pointer;
  }
  .sugg li[aria-selected='true'] {
    background: var(--tm-accent-soft);
  }
  .sn {
    font-size: 13px;
    font-weight: 500;
  }
  .se {
    font-size: 12px;
    color: var(--tm-muted);
  }
</style>
