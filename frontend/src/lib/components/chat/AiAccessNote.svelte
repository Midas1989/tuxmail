<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Chat: which sources of this conversation the AI assistant may see right now (Settings > AI assistant). -->
<script lang="ts">
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { conversationSources } from '../../aiaccess';

  interface Props {
    accountIds: number[];
  }
  let { accountIds }: Props = $props();

  const list = $derived(conversationSources(app.aiSettings, accountIds));
  const shared = $derived(list.filter((s) => s.level !== 'off'));
</script>

{#if list.length}
  <p class="ai-note" data-testid="ai-access-note">
    <ShieldCheck size={12} aria-hidden="true" />
    {#if shared.length}
      <span>{t('chat.ai.sees')}</span>
      {#each list as s, i (s.key)}<span class="src" class:off={s.level === 'off'}>{s.name} · {t(`settings.ai.levels.${s.level}`)}</span>{#if i < list.length - 1},{/if}{/each}
    {:else}
      <span>{t('chat.ai.noAccess')}</span>
    {/if}
    <button class="link" onclick={() => app.openAiSettings()}>{t('chat.ai.manage')}</button>
  </p>
{/if}

<style>
  .ai-note {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    margin: 0 0 6px;
    font-size: 11.5px;
    color: var(--tm-faint);
  }
  .src.off {
    text-decoration: line-through;
  }
  .link {
    border: none;
    background: transparent;
    padding: 0 0 0 4px;
    font: inherit;
    color: var(--tm-accent-text);
    text-decoration: underline;
    cursor: pointer;
  }
</style>
