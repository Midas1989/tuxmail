<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    title: string;
    subtitle?: string;
    actions?: Snippet;
    children: Snippet;
  }
  let { title, subtitle, actions, children }: Props = $props();
</script>

<section class="screen" aria-labelledby="screen-title">
  <header>
    <div>
      <h1 id="screen-title">{title}</h1>
      {#if subtitle}<p>{subtitle}</p>{/if}
    </div>
    {#if actions}<div class="actions">{@render actions()}</div>{/if}
  </header>
  <div class="content">
    {@render children()}
  </div>
</section>

<style>
  .screen {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    background: var(--tm-bg);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 18px 28px 14px;
    background: var(--tm-surface);
    border-bottom: 1px solid var(--tm-line);
  }
  h1 {
    margin: 0;
    font-size: 20px;
    font-weight: 650;
    letter-spacing: -0.015em;
  }
  p {
    margin: 2px 0 0;
    color: var(--tm-muted);
    font-size: 13px;
  }
  .actions {
    display: flex;
    gap: 8px;
  }
  .content {
    position: relative;
    flex: 1;
    min-height: 0;
    overflow: auto;
    padding: 20px 28px 32px;
  }
  @media (max-width: 639px) {
    header {
      padding: 14px 16px 10px;
    }
    .content {
      padding: 14px 16px 80px;
    }
  }
</style>
