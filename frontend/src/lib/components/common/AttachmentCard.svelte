<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import FileText from '@lucide/svelte/icons/file-text';
  import FileIcon from '@lucide/svelte/icons/file';
  import ImageIcon from '@lucide/svelte/icons/image';
  import Calendar from '@lucide/svelte/icons/calendar';
  import Cloud from '@lucide/svelte/icons/cloud';
  import Lock from '@lucide/svelte/icons/lock';
  import Download from '@lucide/svelte/icons/download';
  import CloudUpload from '@lucide/svelte/icons/cloud-upload';
  import { cloud } from '../../state';
  import { attachmentSource } from '../../state/cloud.svelte';
  import type { Attachment } from '../../types';
  import { formatSize, t } from '../../i18n/index.svelte';

  interface Props {
    attachment: Attachment;
    compact?: boolean;
    onown?: boolean;
  }
  let { attachment: a, compact = false, onown = false }: Props = $props();

  const kind = $derived(
    a.cloudLink ? 'cloud' : a.mimeType.startsWith('image/') ? 'image' : a.mimeType === 'text/calendar' ? 'calendar' : a.mimeType.includes('pdf') || a.mimeType.includes('word') ? 'doc' : 'file',
  );
  /** "Save to cloud" (KONZEPT 12.1 #2) for stored mail/chat attachments. */
  const saveSource = $derived(a.cloudLink ? null : attachmentSource(a));
  const canSave = $derived(saveSource != null && cloud.storageAccounts.length > 0);
  // Core-served attachments are downloaded with ?download=1 (Content-Disposition: attachment).
  const href = $derived(a.cloudLink ? (/^https?:\/\//i.test(a.cloudLink.url) ? a.cloudLink.url : undefined) : a.url ? a.url + (a.url.includes('?') ? '&' : '?') + 'download=1' : undefined);
  const ext = $derived((a.filename.split('.').pop() ?? '').toUpperCase().slice(0, 4));
</script>

<div class="card" class:compact class:onown title={a.filename}>
  <span class="thumb thumb-{kind}" aria-hidden="true">
    {#if kind === 'cloud'}
      {#if a.cloudLink?.provider === 'vaultwarden'}<Lock size={18} />{:else}<Cloud size={18} />{/if}
    {:else if kind === 'image'}
      <ImageIcon size={18} />
    {:else if kind === 'calendar'}
      <Calendar size={18} />
    {:else if kind === 'doc'}
      <FileText size={18} />
    {:else}
      <FileIcon size={18} />
    {/if}
    {#if kind !== 'cloud' && ext}<span class="ext">{ext}</span>{/if}
  </span>
  <span class="meta">
    <span class="name">{a.filename}</span>
    <span class="sub">
      {#if a.cloudLink}
        {t(`compose.delivery.${a.cloudLink.provider}`)}
      {:else}
        {formatSize(a.size)}
      {/if}
    </span>
  </span>
  {#if canSave}
    <button class="icon-btn dl" aria-label={t('cloud.save.title', { name: a.filename })} title={t('cloud.save.button')} onclick={() => saveSource && cloud.openSave(saveSource)}>
      <CloudUpload size={16} />
    </button>
  {/if}
  {#if href}
    <a
      class="icon-btn dl"
      {href}
      download={a.cloudLink ? undefined : a.filename}
      target={a.cloudLink ? '_blank' : undefined}
      rel="noopener noreferrer"
      aria-label={t('mail.attachment.download', { name: a.filename })}
      title={t('mail.attachment.download', { name: a.filename })}
    >
      <Download size={16} />
    </a>
  {/if}
</div>

<style>
  .card {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
    max-width: 280px;
    padding: 8px 6px 8px 8px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface);
    color: var(--tm-fg);
    transition: border-color var(--tm-dur);
  }
  .card:hover {
    border-color: var(--tm-line-strong);
  }
  .card.compact {
    padding: 6px 4px 6px 6px;
  }
  .card.onown {
    background: color-mix(in srgb, white 14%, transparent);
    border-color: color-mix(in srgb, white 25%, transparent);
    color: inherit;
  }
  .card.onown .sub,
  .card.onown .dl {
    color: inherit;
    opacity: 0.85;
  }
  .thumb {
    position: relative;
    flex: none;
    width: 36px;
    height: 36px;
    border-radius: 8px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: var(--tm-surface-3);
    color: var(--tm-muted);
  }
  .thumb-doc {
    background: color-mix(in srgb, #ef4444 14%, var(--tm-surface));
    color: #dc2626;
  }
  .thumb-image {
    background: color-mix(in srgb, #8b5cf6 16%, var(--tm-surface));
    color: #7c3aed;
  }
  .thumb-calendar {
    background: color-mix(in srgb, #f59e0b 18%, var(--tm-surface));
    color: #b45309;
  }
  .thumb-cloud {
    background: color-mix(in srgb, #0ea5e9 16%, var(--tm-surface));
    color: #0284c7;
  }
  .ext {
    position: absolute;
    bottom: -4px;
    right: -6px;
    font-size: 8.5px;
    font-weight: 700;
    padding: 0 3px;
    border-radius: 3px;
    background: var(--tm-surface);
    border: 1px solid var(--tm-line);
    color: var(--tm-muted);
  }
  .meta {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1;
  }
  .name {
    font-size: 13px;
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .sub {
    font-size: 11.5px;
    color: var(--tm-muted);
  }
  .dl {
    width: 28px;
    height: 28px;
  }
</style>
