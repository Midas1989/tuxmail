<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Messenger composer: growing textarea, Enter sends (configurable), emoji,
  "+" menu (file, photo, cloud, full e-mail editor), voice note when empty,
  channel chip with quick switch. For mail the subject ("Re: …") is
  generated and hidden; the identity's chat signature is shown as a
  collapsed line that can be expanded, switched or removed per message.
-->
<script lang="ts">
  import SendHorizontal from '@lucide/svelte/icons/send-horizontal';
  import { onMount } from 'svelte';
  import { updateWatch } from '../../state/updateWatch.svelte';
  import Plus from '@lucide/svelte/icons/plus';
  import Smile from '@lucide/svelte/icons/face-slightly-smiling';
  import Mic from '@lucide/svelte/icons/mic';
  import X from '@lucide/svelte/icons/x';
  import Trash from '@lucide/svelte/icons/trash';
  import FileIcon from '@lucide/svelte/icons/file';
  import ImageIcon from '@lucide/svelte/icons/image';
  import Cloud from '@lucide/svelte/icons/cloud';
  import Maximize2 from '@lucide/svelte/icons/maximize-2';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import Reply from '@lucide/svelte/icons/reply';
  import Clock from '@lucide/svelte/icons/clock';
  import Sparkles from '@lucide/svelte/icons/sparkles';
  import CalendarClock from '@lucide/svelte/icons/calendar-clock';
  import { longpress } from '../../actions';
  import { detectTrigger, fillSnippet, fuzzy, timePresets, toLocalInput, type Trigger } from '../../chatfeatures';
  import type { Address, Snippet } from '../../types';
  import Modal from '../common/Modal.svelte';
  import Avatar from '../common/Avatar.svelte';
  import MeetingDialog from './MeetingDialog.svelte';
  import { fmt, i18n } from '../../i18n/index.svelte';
  import { app, prefs } from '../../state';
  import type { ChannelOption } from '../../state';
  import { textToHtml } from '../../state/app.svelte';
  import { t } from '../../i18n/index.svelte';
  import type { OutgoingAttachment, SendFinding, Signature } from '../../types';
  import SendChecks from '../compose/SendChecks.svelte';
  import ChannelIcon from '../common/ChannelIcon.svelte';
  import ContextMenu, { type MenuItem } from '../common/ContextMenu.svelte';
  import EmojiPicker from './EmojiPicker.svelte';

  let text = $state('');
  let sending = $state(false);
  let selectedKey = $state<string | null>(null);
  let textarea = $state<HTMLTextAreaElement>();
  let fileInput = $state<HTMLInputElement>();
  let photoInput = $state<HTMLInputElement>();
  let emojiOpen = $state(false);
  let plusMenu = $state<{ x: number; y: number } | null>(null);
  let channelMenu = $state<{ x: number; y: number } | null>(null);
  let pending = $state<{ key: string; file: File; url?: string }[]>([]);

  // signature for this message: undefined = identity default, null = removed, number = chosen signature
  let sigChoice = $state<number | null | undefined>(undefined);
  let sigOpen = $state(false);

  // voice recording
  let recording = $state(false);
  let recSeconds = $state(0);
  let recTimer: ReturnType<typeof setInterval> | null = null;
  let recorder: MediaRecorder | null = null;
  let chunks: Blob[] = [];
  let stream: MediaStream | null = null;

  // drafts, AI suggestions, "/" and "@", scheduling, meeting proposals
  let suggestions = $state<string[]>([]);
  let trigger = $state<Trigger | null>(null);
  let triggerIndex = $state(0);
  let mentionCc = $state<Address[]>([]);
  let sendMenu = $state<{ x: number; y: number } | null>(null);
  let scheduleOpen = $state(false);
  let scheduleCustom = $state(toLocalInput(new Date(Date.now() + 2 * 3600_000)));
  let meetingOpen = $state(false);
  let draftTimer: ReturnType<typeof setTimeout> | null = null;
  let pendingDraft: (() => Promise<void>) | null = null;
  let loadedFor: number | null = null;

  const options = $derived(app.selectedConversationId != null ? app.channelOptions() : []);
  $effect(() => {
    const id = app.selectedConversationId;
    selectedKey = null;
    text = '';
    pending = [];
    sigChoice = undefined;
    sigOpen = false;
    mentionCc = [];
    suggestions = [];
    trigger = null;
    loadedFor = id;
    if (id == null) return;
    void app.api.getDraft(id).then((d) => {
      if (loadedFor !== id || !d) return;
      text = d.text;
      if (d.channelKey) selectedKey = d.channelKey;
      queueMicrotask(autosize);
    });
  });

  // Reply suggestions when the last message is from the other side.
  $effect(() => {
    const id = app.selectedConversationId;
    const last = app.items.filter((i) => !i.scheduledFor && !i.system).at(-1);
    if (id == null || !last || last.isOwn || !app.aiReady) {
      suggestions = [];
      return;
    }
    void app.api.aiSuggestReplies(id).then((s) => {
      if (app.selectedConversationId === id) suggestions = s;
    }, () => (suggestions = []));
  });

  function saveDraftSoon() {
    const id = app.selectedConversationId;
    if (id == null) return;
    if (draftTimer) clearTimeout(draftTimer);
    const snapshot = { text, replyToItemId: app.replyingTo?.id, channelKey: selectedKey ?? undefined };
    pendingDraft = () => app.api.saveDraft(id, snapshot.text.trim() ? snapshot : null);
    draftTimer = setTimeout(() => {
      const save = pendingDraft;
      pendingDraft = null;
      void save?.();
    }, 500);
  }

  /** Before the page is reloaded for an update: the draft is saved now, not after the pause. */
  async function flushDraft() {
    if (draftTimer) clearTimeout(draftTimer);
    draftTimer = null;
    const save = pendingDraft;
    pendingDraft = null;
    await save?.();
  }
  onMount(() => updateWatch.onBeforeReload(flushDraft));

  const participants = $derived((app.conversation?.participantIds ?? []).map((id) => app.person(id)).filter((p) => !!p));
  const triggerList = $derived.by((): { id: string; label: string; detail: string; snippet?: Snippet; personId?: number }[] => {
    if (!trigger) return [];
    if (trigger.kind === 'slash') {
      return app.snippets
        .map((sn) => ({ sn, score: Math.max(fuzzy(sn.trigger, trigger!.query), fuzzy(sn.title, trigger!.query) - 5) }))
        .filter((x) => x.score >= 0)
        .sort((a, b) => b.score - a.score)
        .slice(0, 8)
        .map(({ sn }) => ({ id: sn.id, label: '/' + sn.trigger, detail: sn.title, snippet: sn }));
    }
    if (!(app.conversation && (app.conversation.isGroup || app.conversation.participantIds.length > 1))) return [];
    return participants
      .filter((p) => fuzzy(p!.displayName, trigger!.query) >= 0)
      .slice(0, 8)
      .map((p) => ({ id: 'p' + p!.id, label: p!.displayName, detail: p!.handles.find((h) => h.kind === 'email')?.value ?? '', personId: p!.id }));
  });

  function updateTrigger() {
    if (!textarea) return;
    trigger = detectTrigger(text, textarea.selectionStart ?? text.length);
    triggerIndex = 0;
  }

  function acceptTrigger(entry: (typeof triggerList)[number]) {
    if (!trigger) return;
    let insert = '';
    if (entry.snippet) {
      const p = app.person(app.conversation?.participantIds[0] ?? -1);
      const me = app.identities.find((i) => i.id === option?.identityId) ?? app.identities[0];
      insert = fillSnippet(entry.snippet.text, { firstName: app.conversation?.isGroup ? undefined : p?.displayName.split(' ')[0], name: p?.displayName, myName: me?.name, locale: i18n.locale });
    } else if (entry.personId != null) {
      const p = app.person(entry.personId)!;
      insert = '@' + p.displayName.split(' ')[0] + ' ';
      const email = p.handles.find((h) => h.kind === 'email')?.value;
      if (isMail && email && !mentionCc.some((a) => a.email === email)) mentionCc = [...mentionCc, { name: p.displayName, email }];
    }
    const pos = trigger.from + insert.length;
    text = text.slice(0, trigger.from) + insert + text.slice(trigger.to);
    trigger = null;
    queueMicrotask(() => {
      textarea?.focus();
      textarea?.setSelectionRange(pos, pos);
      autosize();
    });
    saveDraftSoon();
  }
  const option = $derived<ChannelOption | undefined>(options.find((o) => o.key === selectedKey) ?? options[0]);
  const isMail = $derived(option?.channel === 'email');
  const signature = $derived.by((): Signature | null => {
    if (!isMail || !option?.identityId) return null;
    if (sigChoice === null) return null;
    if (typeof sigChoice === 'number') return app.signatures.find((s) => s.id === sigChoice) ?? null;
    return app.chatSignatureFor(option.identityId) ?? null;
  });
  const sigPreview = $derived(signature ? signature.plain.split('\n').filter((l) => l.trim() && !/^(mit freundlichen|viele|liebe|beste|best|kind) /i.test(l)).slice(0, 3).join(' · ') : '');
  const empty = $derived(!text.trim() && pending.length === 0);

  $effect(() => {
    if (app.replyingTo) textarea?.focus();
  });

  function autosize() {
    if (!textarea) return;
    textarea.style.height = 'auto';
    textarea.style.height = Math.min(textarea.scrollHeight, 160) + 'px';
  }

  function chipLabel(o: ChannelOption): string {
    const ident = o.identityId != null ? app.identities.find((i) => i.id === o.identityId) : undefined;
    return ident ? ident.email : o.label;
  }

  async function uploadAll(): Promise<OutgoingAttachment[]> {
    const out: OutgoingAttachment[] = [];
    for (const p of pending) {
      const { uploadToken } = await app.api.uploadAttachment(p.file, p.file.name);
      out.push({ uploadToken, filename: p.file.name, mimeType: p.file.type || 'application/octet-stream', size: p.file.size, delivery: 'attach' });
    }
    return out;
  }

  /** A send blocked by send checks, waiting for the user's decision. */
  let blocked = $state<{ findings: SendFinding[]; retry: (checks: string[]) => Promise<void> } | null>(null);

  async function send(sendAt?: string) {
    if (!option || empty || sending) return;
    sending = true;
    // A pending autosave would bring the sent text back as draft.
    if (draftTimer) clearTimeout(draftTimer);
    draftTimer = null;
    const body = text;
    const replyTo = app.replyingTo;
    try {
      const attachments = await uploadAll();
      text = '';
      pending = [];
      app.replyingTo = null;
      queueMicrotask(autosize);
      const cc = mentionCc;
      mentionCc = [];
      suggestions = [];
      if (app.selectedConversationId != null) void app.api.saveDraft(app.selectedConversationId, null);
      const opt = option;
      const extra = { replyTo, signature, attachments, cc, sendAt };
      const res = await app.sendChat(body, opt, extra);
      if (res?.blocked) {
        // Send checks (KONZEPT 8.13): keep the text and ask before sending.
        text = body;
        app.replyingTo = replyTo;
        queueMicrotask(autosize);
        blocked = {
          findings: res.findings ?? [],
          retry: async (checks) => {
            blocked = null;
            text = '';
            app.replyingTo = null;
            queueMicrotask(autosize);
            try {
              await app.sendChat(body, opt, { ...extra, confirmChecks: checks });
              sigChoice = undefined;
            } catch (err) {
              text = body;
              app.toast(t('compose.sendFailed', { error: err instanceof Error ? err.message : String(err) }), undefined, 'error');
            }
          },
        };
        return;
      }
      sigChoice = undefined;
    } catch (err) {
      text = body;
      app.toast(t('compose.sendFailed', { error: err instanceof Error ? err.message : String(err) }), undefined, 'error');
    } finally {
      sending = false;
      textarea?.focus();
    }
  }

  async function expandToFullMail() {
    const lastMail = app.replyingTo?.messageId != null ? app.replyingTo : [...app.items].reverse().find((i) => i.messageId != null);
    const html = text ? `<p>${textToHtml(text)}</p>` : '';
    if (lastMail?.messageId != null) {
      const original = await app.api.getMessage(lastMail.messageId);
      const init = app.replyInit(original, app.conversation?.isGroup ? 'replyAll' : 'reply', html);
      if (isMail && option?.identityId) init.identityId = option.identityId;
      app.openCompose(init);
    } else {
      const p = app.person(app.conversation?.participantIds[0] ?? -1);
      const email = p?.handles.find((h) => h.kind === 'email')?.value;
      app.openCompose({ mode: 'new', to: email ? [{ name: p?.displayName, email }] : [], bodyHtml: html, identityId: option?.identityId });
    }
    text = '';
  }

  function addFiles(list: FileList | null) {
    for (const f of list ?? []) {
      pending.push({ key: `${f.name}-${f.size}-${Math.random().toString(36).slice(2, 6)}`, file: f, url: f.type.startsWith('image/') ? URL.createObjectURL(f) : undefined });
    }
    textarea?.focus();
  }

  function onkeydown(e: KeyboardEvent) {
    if (trigger && triggerList.length) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        triggerIndex = (triggerIndex + (e.key === 'ArrowDown' ? 1 : -1) + triggerList.length) % triggerList.length;
        return;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        acceptTrigger(triggerList[triggerIndex]);
        return;
      }
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        trigger = null;
        return;
      }
    }
    if (e.key !== 'Enter' || e.isComposing) return;
    const sendNow = prefs.chatEnterSends ? !e.shiftKey && !e.ctrlKey : e.ctrlKey || e.metaKey;
    if (sendNow) {
      e.preventDefault();
      void send();
    }
  }

  function insertEmoji(emoji: string) {
    const el = textarea;
    if (!el) {
      text += emoji;
      return;
    }
    const s = el.selectionStart ?? text.length;
    const end = el.selectionEnd ?? text.length;
    text = text.slice(0, s) + emoji + text.slice(end);
    emojiOpen = false;
    queueMicrotask(() => {
      el.focus();
      el.setSelectionRange(s + emoji.length, s + emoji.length);
      autosize();
    });
  }

  const plusItems = $derived<MenuItem[]>([
    { id: 'file', label: t('chat.composer.file'), icon: FileIcon, run: () => fileInput?.click() },
    { id: 'photo', label: t('chat.composer.photo'), icon: ImageIcon, run: () => photoInput?.click() },
    { id: 'nextcloud', label: t('chat.composer.fromNextcloud'), icon: Cloud, run: () => app.toast(t('chat.composer.cloudSoon')) },
    { id: 'opencloud', label: t('chat.composer.fromOpenCloud'), icon: Cloud, run: () => app.toast(t('chat.composer.cloudSoon')) },
    { id: 'meeting', label: t('chat.meeting.title'), icon: CalendarClock, run: () => void (meetingOpen = true) },
    ...(options.some((o) => o.channel === 'email') ? [{ id: 'full', label: t('chat.composer.fullMail'), icon: Maximize2, run: () => expandToFullMail() }] : []),
  ]);

  const channelItems = $derived<MenuItem[]>(
    options.map((o) => ({
      id: o.key,
      label: `${t(`common.channel.${o.channel}`)} · ${chipLabel(o)}`,
      run: () => {
        selectedKey = o.key;
        sigChoice = undefined;
      },
    })),
  );

  // ----- voice notes ---------------------------------------------------------

  async function startRecording() {
    recording = true;
    recSeconds = 0;
    chunks = [];
    recTimer = setInterval(() => recSeconds++, 1000);
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      recorder = new MediaRecorder(stream);
      recorder.ondataavailable = (e) => chunks.push(e.data);
      recorder.start();
    } catch {
      recorder = null; // no microphone / permission: demo recording (silent note)
    }
  }

  function stopTracks() {
    if (recTimer) clearInterval(recTimer);
    recTimer = null;
    stream?.getTracks().forEach((tr) => tr.stop());
    stream = null;
  }

  function cancelRecording() {
    recorder?.stop();
    recorder = null;
    stopTracks();
    recording = false;
  }

  async function finishRecording() {
    const seconds = Math.max(1, recSeconds);
    let blob: Blob;
    if (recorder) {
      const done = new Promise<void>((r) => (recorder!.onstop = () => r()));
      recorder.stop();
      await done;
      blob = new Blob(chunks, { type: recorder.mimeType || 'audio/webm' });
    } else {
      blob = new Blob([new Uint8Array(44)], { type: 'audio/wav' });
    }
    recorder = null;
    stopTracks();
    recording = false;
    if (!option) return;
    const name = `${t('chat.media.voice')}.${blob.type.includes('webm') ? 'webm' : 'wav'}`;
    const { uploadToken } = await app.api.uploadAttachment(blob, name);
    await app.sendChat('', option, { attachments: [{ uploadToken, filename: name, mimeType: blob.type, size: blob.size, delivery: 'attach', durationSec: seconds }], replyTo: app.replyingTo });
    app.replyingTo = null;
  }

  function fmtSec(s: number) {
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
  }
</script>

<div class="composer" role="group" aria-label={t('chat.composerLabel')}>
  {#if blocked}
    {@const b = blocked}
    <SendChecks compact findings={b.findings} oncancel={() => (blocked = null)} onconfirm={(c) => void b.retry(c)} />
  {/if}
  {#if app.replyingTo}
    {@const r = app.replyingTo}
    <div class="reply-bar">
      <Reply size={16} aria-hidden="true" />
      <div class="rb-text">
        <strong>{r.isOwn ? t('chat.you') : r.senderName}</strong>
        <span>{r.body || r.card?.title || r.attachments[0]?.filename || ''}</span>
      </div>
      <button class="icon-btn" aria-label={t('chat.cancelReply')} onclick={() => (app.replyingTo = null)}><X size={16} /></button>
    </div>
  {/if}

  {#if pending.length}
    <div class="pending">
      {#each pending as p, i (p.key)}
        <span class="pend">
          {#if p.url}<img src={p.url} alt="" />{:else}<FileIcon size={16} />{/if}
          <span class="pname">{p.file.name}</span>
          <button aria-label={t('compose.removeAttachment', { name: p.file.name })} onclick={() => pending.splice(i, 1)}><X size={12} /></button>
        </span>
      {/each}
    </div>
  {/if}

  {#if suggestions.length && !text}
    <div class="ai-chips" role="group" aria-label={t('chat.ai.suggestions')}>
      <Sparkles size={14} aria-hidden="true" />
      {#each suggestions as sg (sg)}
        <button
          class="ai-chip"
          onclick={() => {
            text = sg;
            queueMicrotask(() => {
              textarea?.focus();
              autosize();
            });
          }}>{sg}</button
        >
      {/each}
    </div>
  {:else if !app.aiReady && !text && app.items.at(-1) && !app.items.at(-1)?.isOwn}
    <div class="ai-chips">
      <button class="ai-hint" onclick={() => app.openAiSettings()}><Sparkles size={13} /> {t('chat.ai.enableHint')}</button>
    </div>
  {/if}

  {#if mentionCc.length && isMail}
    <div class="cc-chips">
      <span class="cc-label">{t('chat.mention.cc')}</span>
      {#each mentionCc as a (a.email)}
        <span class="cc-chip" title={a.email}>{a.name ?? a.email}<button aria-label={t('compose.removeRecipient', { name: a.name ?? a.email })} onclick={() => (mentionCc = mentionCc.filter((x) => x.email !== a.email))}><X size={11} /></button></span>
      {/each}
    </div>
  {/if}

  {#if trigger && triggerList.length}
    <ul class="trigger-pop" role="listbox" id="composer-trigger" aria-label={trigger.kind === 'slash' ? t('chat.snippets.title') : t('chat.mention.title')}>
      {#each triggerList as entry, i (entry.id)}
        <li
          id="trig-{i}"
          role="option"
          aria-selected={i === triggerIndex}
          onmousedown={(e) => {
            e.preventDefault();
            acceptTrigger(entry);
          }}
          onmousemove={() => (triggerIndex = i)}
        >
          {#if entry.personId != null}<Avatar name={entry.label} seed={entry.personId} size={24} />{/if}
          <span class="t-label">{entry.label}</span>
          <span class="t-detail">{entry.snippet ? entry.snippet.text.slice(0, 60) : entry.detail}</span>
        </li>
      {/each}
    </ul>
  {/if}

  {#if recording}
    <div class="rec-bar" role="status">
      <button class="icon-btn" aria-label={t('chat.composer.cancelVoice')} onclick={cancelRecording}><Trash size={18} /></button>
      <span class="rec-dot" aria-hidden="true"></span>
      <span class="rec-time">{fmtSec(recSeconds)}</span>
      <span class="rec-label">{t('chat.composer.recording')}</span>
      <button class="send" aria-label={t('chat.composer.sendVoice')} onclick={finishRecording}><SendHorizontal size={18} /></button>
    </div>
  {:else}
    <div class="row">
      <div class="left">
        <div class="emoji-wrap">
          <button class="icon-btn" aria-label={t('chat.emoji.title')} aria-expanded={emojiOpen} onclick={() => (emojiOpen = !emojiOpen)}><Smile size={20} /></button>
          {#if emojiOpen}<EmojiPicker onpick={insertEmoji} onclose={() => (emojiOpen = false)} />{/if}
        </div>
        <button class="icon-btn" aria-label={t('chat.composer.attach')} aria-haspopup="menu" onclick={(e) => (plusMenu = { x: e.clientX, y: e.clientY - 10 })}><Plus size={20} /></button>
      </div>
      <div class="field">
        {#if option}
          <button
            class="chip ch-{option.channel}"
            title={t('chat.channel')}
            aria-label={t('chat.channelChip', { channel: t(`common.channel.${option.channel}`), account: chipLabel(option) })}
            aria-haspopup="menu"
            onclick={(e) => {
              const r = (e.currentTarget as HTMLElement).getBoundingClientRect();
              channelMenu = { x: r.left, y: r.top - 8 - options.length * 34 - 12 };
            }}
          >
            <ChannelIcon channel={option.channel} size={13} />
            <span class="chip-label">{chipLabel(option)}</span>
            <ChevronDown size={12} aria-hidden="true" />
          </button>
        {/if}
        <textarea
          bind:this={textarea}
          bind:value={text}
          rows="1"
          placeholder={isMail ? t('chat.placeholderMail') : t('chat.placeholder')}
          aria-label={t('chat.placeholder')}
          oninput={() => {
            autosize();
            updateTrigger();
            saveDraftSoon();
          }}
          onkeyup={(e) => ['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key) && updateTrigger()}
          onclick={updateTrigger}
          onblur={() => setTimeout(() => (trigger = null), 150)}
          {onkeydown}
          role="combobox"
          aria-expanded={!!trigger && triggerList.length > 0}
          aria-controls="composer-trigger"
          aria-activedescendant={trigger && triggerList.length ? `trig-${triggerIndex}` : undefined}
          aria-autocomplete="list"
          data-chat-input
        ></textarea>
      </div>
      {#if empty}
        <button class="send mic" aria-label={t('chat.composer.voice')} title={t('chat.composer.voice')} onclick={startRecording} disabled={!option}><Mic size={19} /></button>
      {:else}
        <button
          class="send"
          disabled={sending || !option}
          onclick={() => send()}
          use:longpress={(x, y) => (sendMenu = { x: x - 200, y: y - 100 })}
          aria-haspopup="menu"
          aria-label={t('common.action.send')} title={prefs.chatEnterSends ? t('common.action.send') + ' (Enter)' : t('common.action.send') + ' (Ctrl+Enter)'}><SendHorizontal size={18} /></button>
      {/if}
    </div>
    {#if isMail}
      <div class="sig-line">
        {#if signature}
          <button class="sig-toggle" aria-expanded={sigOpen} onclick={() => (sigOpen = !sigOpen)} title={t('chat.signature.expand')}>
            — {t('chat.signature.label')}: <span class="sig-prev">{sigPreview || signature.name}</span>
            <ChevronDown size={12} aria-hidden="true" />
          </button>
        {:else}
          <span class="sig-none">— {t('chat.signature.none')}</span>
        {/if}
        <label class="sr-only" for="chat-sig-select">{t('chat.signature.switch')}</label>
        <select id="chat-sig-select" class="sig-select" value={signature?.id ?? ''} onchange={(e) => {
          const v = (e.currentTarget as HTMLSelectElement).value;
          sigChoice = v === '' ? null : Number(v);
        }} title={t('chat.signature.switch')}>
          <option value="">{t('chat.signature.noneOption')}</option>
          {#each app.signatures as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
        </select>
        {#if signature}
          <button class="icon-btn sig-x" aria-label={t('chat.signature.remove')} title={t('chat.signature.remove')} onclick={() => (sigChoice = null)}><X size={13} /></button>
        {/if}
      </div>
      {#if sigOpen && signature}
        <pre class="sig-full">{signature.plain}</pre>
      {/if}
    {/if}
  {/if}
  <input type="file" multiple hidden bind:this={fileInput} onchange={(e) => addFiles((e.currentTarget as HTMLInputElement).files)} />
  <input type="file" multiple hidden accept="image/*,video/*" bind:this={photoInput} onchange={(e) => addFiles((e.currentTarget as HTMLInputElement).files)} />
</div>

{#if sendMenu}
  <ContextMenu
    x={sendMenu.x}
    y={sendMenu.y}
    label={t('common.action.send')}
    onclose={() => (sendMenu = null)}
    items={[
      { id: 'now', label: t('common.action.send'), icon: SendHorizontal, run: () => send() },
      { id: 'later', label: t('chat.schedule.later'), icon: Clock, run: () => void (scheduleOpen = true) },
    ]}
  />
{/if}
{#if scheduleOpen}
  <Modal title={t('chat.schedule.later')} onclose={() => (scheduleOpen = false)} width="380px">
    <div class="presets">
      {#each timePresets(new Date()) as p (p.id)}
        <button
          class="preset"
          onclick={() => {
            scheduleOpen = false;
            void send(p.at.toISOString());
          }}><span>{t(`chat.time.${p.id}`)}</span><small>{fmt().dateTime(p.at)}</small></button
        >
      {/each}
    </div>
    <label><span class="label">{t('chat.time.custom')}</span><input class="input" type="datetime-local" bind:value={scheduleCustom} /></label>
    {#snippet footer()}
      <button class="btn" onclick={() => (scheduleOpen = false)}>{t('common.action.cancel')}</button>
      <button
        class="btn btn-primary"
        onclick={() => {
          scheduleOpen = false;
          void send(new Date(scheduleCustom).toISOString());
        }}><Clock size={14} /> {t('chat.schedule.action')}</button
      >
    {/snippet}
  </Modal>
{/if}
{#if meetingOpen && option}
  <MeetingDialog {option} onclose={() => (meetingOpen = false)} />
{/if}
{#if plusMenu}
  <ContextMenu x={plusMenu.x} y={plusMenu.y - 44 * plusItems.length} items={plusItems} label={t('chat.composer.attach')} onclose={() => (plusMenu = null)} width={250} />
{/if}
{#if channelMenu}
  <ContextMenu x={channelMenu.x} y={channelMenu.y} items={channelItems} label={t('chat.channel')} onclose={() => (channelMenu = null)} width={290} />
{/if}

<style>
  .composer {
    position: relative;
    padding: 8px 12px 10px;
    background: var(--tm-surface-2);
    border-top: 1px solid var(--tm-line);
  }
  .ai-chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    margin-bottom: 8px;
    color: var(--tm-brand);
  }
  .ai-chip {
    padding: 5px 12px;
    border-radius: 99px;
    border: 1px solid color-mix(in srgb, var(--tm-brand) 45%, var(--tm-line));
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
    font-size: 13px;
  }
  .ai-chip:hover {
    background: color-mix(in srgb, var(--tm-brand) 12%, var(--tm-surface));
  }
  .ai-hint {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    border: none;
    background: transparent;
    color: var(--tm-faint);
    font: inherit;
    font-size: 12px;
    padding: 0;
  }
  .ai-hint:hover {
    color: var(--tm-accent-text);
  }
  .cc-chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    margin-bottom: 6px;
    font-size: 12px;
  }
  .cc-label {
    color: var(--tm-muted);
    font-weight: 600;
  }
  .cc-chip {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    height: 22px;
    padding: 0 4px 0 9px;
    border-radius: 99px;
    background: var(--tm-accent-soft);
    color: var(--tm-accent-text);
  }
  .cc-chip button {
    display: inline-flex;
    border: none;
    background: transparent;
    color: inherit;
    padding: 2px;
  }
  .trigger-pop {
    position: absolute;
    left: 60px;
    right: 60px;
    bottom: calc(100% - 4px);
    z-index: 20;
    list-style: none;
    margin: 0;
    padding: 4px;
    border-radius: 12px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    box-shadow: var(--tm-shadow-lg);
    max-height: 260px;
    overflow-y: auto;
  }
  .trigger-pop li {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 10px;
    border-radius: 8px;
    cursor: pointer;
    font-size: 13px;
  }
  .trigger-pop li[aria-selected='true'] {
    background: var(--tm-accent-soft);
  }
  .t-label {
    font-weight: 650;
    font-family: var(--tm-mono);
  }
  .t-detail {
    color: var(--tm-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .presets {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-bottom: 12px;
  }
  .preset {
    display: flex;
    justify-content: space-between;
    padding: 10px 12px;
    border: 1px solid var(--tm-line);
    border-radius: 10px;
    background: var(--tm-surface);
    color: var(--tm-fg);
    font: inherit;
  }
  .preset:hover {
    background: var(--tm-surface-2);
  }
  .preset small {
    color: var(--tm-muted);
  }
  .reply-bar {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 0 0 8px;
    padding: 6px 6px 6px 12px;
    border-left: 4px solid var(--tm-brand);
    border-radius: 8px;
    background: var(--tm-surface);
    color: var(--tm-muted);
  }
  .rb-text {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    font-size: 13px;
  }
  .rb-text strong {
    color: var(--tm-accent-text);
  }
  .rb-text span {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .pending {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin-bottom: 8px;
  }
  .pend {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 36px;
    padding: 0 6px 0 4px;
    border-radius: 10px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    font-size: 12.5px;
  }
  .pend img {
    width: 28px;
    height: 28px;
    object-fit: cover;
    border-radius: 6px;
  }
  .pname {
    max-width: 160px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .pend button {
    display: inline-flex;
    border: none;
    background: transparent;
    color: var(--tm-muted);
  }
  .row {
    display: flex;
    align-items: flex-end;
    gap: 6px;
  }
  .left {
    display: flex;
    align-items: center;
    padding-bottom: 4px;
  }
  .emoji-wrap {
    position: relative;
  }
  .field {
    flex: 1;
    min-width: 0;
    display: flex;
    align-items: flex-end;
    gap: 6px;
    padding: 5px 8px 5px 6px;
    border-radius: 22px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    transition: border-color var(--tm-dur);
  }
  .field:focus-within {
    border-color: var(--tm-accent);
  }
  .chip {
    flex: none;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    height: 26px;
    max-width: 170px;
    margin-bottom: 2px;
    padding: 0 8px;
    border-radius: 99px;
    border: none;
    background: var(--tm-surface-2);
    color: var(--tm-muted);
    font: inherit;
    font-size: 11.5px;
  }
  .chip:hover {
    color: var(--tm-fg);
  }
  .chip-label {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  textarea {
    flex: 1;
    min-width: 0;
    resize: none;
    border: none;
    background: transparent;
    color: var(--tm-fg);
    font: inherit;
    font-size: 14.5px;
    line-height: 1.4;
    padding: 5px 2px;
    max-height: 160px;
    outline: none;
  }
  .send {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 42px;
    height: 42px;
    border-radius: 99px;
    border: none;
    background: var(--tm-accent);
    color: var(--tm-accent-fg);
    transition: transform var(--tm-dur);
  }
  .send:disabled {
    opacity: 0.45;
  }
  .send:not(:disabled):active {
    transform: scale(0.94);
  }
  .sig-line {
    display: flex;
    align-items: center;
    gap: 4px;
    margin: 4px 50px 0 82px;
    font-size: 11.5px;
    color: var(--tm-faint);
    min-width: 0;
  }
  .sig-toggle {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    min-width: 0;
    border: none;
    background: transparent;
    color: inherit;
    font: inherit;
    padding: 0;
  }
  .sig-prev {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 320px;
  }
  .sig-select {
    margin-left: auto;
    max-width: 130px;
    height: 22px;
    border: 1px solid var(--tm-line);
    border-radius: 6px;
    background: var(--tm-surface);
    color: var(--tm-muted);
    font: inherit;
    font-size: 11.5px;
  }
  .sig-x {
    width: 22px;
    height: 22px;
  }
  .sig-full {
    margin: 4px 50px 0 82px;
    padding: 6px 10px;
    border-radius: 8px;
    background: var(--tm-surface);
    border: 1px dashed var(--tm-line-strong);
    font: inherit;
    font-size: 12px;
    color: var(--tm-muted);
    white-space: pre-wrap;
  }
  .rec-bar {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 4px 0;
  }
  .rec-dot {
    width: 10px;
    height: 10px;
    border-radius: 99px;
    background: var(--tm-danger);
    animation: blink 1s infinite;
  }
  @keyframes blink {
    50% {
      opacity: 0.3;
    }
  }
  .rec-time {
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }
  .rec-label {
    flex: 1;
    color: var(--tm-muted);
    font-size: 13px;
  }
  @media (max-width: 639px), (pointer: coarse) and (max-height: 500px) {
    .sig-line,
    .sig-full {
      margin-left: 8px;
      margin-right: 8px;
    }
    .chip-label {
      display: none;
    }
  }
</style>
