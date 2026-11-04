<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings → Allgemein → Tastenkürzel (reference list). -->
<script lang="ts">
  import { t } from '../../i18n/index.svelte';

  const shortcuts: [string, string][] = $derived([
    ['Ctrl+K', t('settings.shortcut.palette')],
    ['Ctrl+Shift+V', t('settings.shortcut.toggleView')],
    ['J / K', t('settings.shortcut.nextPrev')],
    ['Enter', t('settings.shortcut.open')],
    ['Esc', t('settings.shortcut.close')],
    ['C', t('settings.shortcut.compose')],
    ['R / Shift+R / F', t('settings.shortcut.reply')],
    ['E / Entf', t('settings.shortcut.archiveDelete')],
    ['S', t('settings.shortcut.flag')],
    ['T', t('settings.shortcut.tag')],
    ['/', t('settings.shortcut.search')],
    ['Ctrl+Enter', t('settings.shortcut.send')],
  ]);
</script>

<section class="card" id="set-keys" aria-labelledby="set-keys-h">
  <h2 id="set-keys-h">{t('settings.shortcuts')}</h2>
  <p class="intro">{t('settings.pages.general.shortcutsIntro')}</p>
  <dl class="keys">
    {#each shortcuts as [k, d] (k)}
      <dt><span class="kbd">{k}</span></dt>
      <dd>{d}</dd>
    {/each}
  </dl>
</section>

<style>
  .keys {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 8px 16px;
    margin: 0;
    font-size: 13px;
  }
  .keys dd {
    margin: 0;
    color: var(--tm-muted);
  }
</style>
