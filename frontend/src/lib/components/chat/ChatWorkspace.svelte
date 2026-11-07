<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Chat mode: activity bar | chat list | conversation (| info panel).
  No folders, no mail toolbar. On phones: list -> full-screen chat with back button.
-->
<script lang="ts">
  import { app } from '../../state';
  import ChatList from './ChatList.svelte';
  import Conversation from './Conversation.svelte';
  import InfoPanel from './InfoPanel.svelte';
  import OriginalMailDialog from './OriginalMailDialog.svelte';
  import ForwardDialog from './ForwardDialog.svelte';
  import ChatDialogs from './ChatDialogs.svelte';
</script>

<div class="ws" class:has-selection={app.selectedConversationId != null} class:info={app.infoOpen && app.conversation}>
  <div class="list"><ChatList /></div>
  <div class="conv"><Conversation /></div>
  {#if app.infoOpen && app.conversation}
    <div class="side"><InfoPanel /></div>
  {/if}
</div>

{#if app.originalMessage}
  <OriginalMailDialog message={app.originalMessage} />
{/if}
<ChatDialogs />
{#if app.forwarding}
  <ForwardDialog item={app.forwarding} />
{/if}

<style>
  .ws {
    flex: 1;
    min-width: 0;
    min-height: 0;
    display: grid;
    grid-template-columns: minmax(300px, 380px) 1fr;
    grid-template-rows: minmax(0, 1fr);
    overflow: hidden;
  }
  .ws.info {
    grid-template-columns: minmax(300px, 380px) 1fr minmax(280px, 340px);
  }
  .list,
  .conv,
  .side {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
  }
  .list > :global(*),
  .conv > :global(*),
  .side > :global(*) {
    flex: 1;
  }
  .list {
    border-right: 1px solid var(--tm-line);
  }
  @media (max-width: 1199px) {
    .ws.info {
      grid-template-columns: minmax(280px, 340px) 1fr;
    }
    .side {
      position: fixed;
      top: 0;
      right: 0;
      bottom: 0;
      z-index: 40;
      width: min(360px, 100vw);
      padding: var(--tm-safe-top) var(--tm-safe-right) var(--tm-safe-bottom) 0;
      box-sizing: border-box;
      background: var(--tm-bg);
      box-shadow: var(--tm-shadow-lg);
    }
  }
  @media (max-width: 899px) {
    .ws,
    .ws.info {
      grid-template-columns: 1fr;
    }
    .list {
      border-right: none;
    }
    .ws.has-selection .list,
    .ws:not(.has-selection) .conv {
      display: none;
    }
  }
  @media (max-width: 639px) {
    .side {
      width: 100vw;
      bottom: 60px;
    }
  }
</style>
