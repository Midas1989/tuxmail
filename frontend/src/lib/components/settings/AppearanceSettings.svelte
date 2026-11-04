<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Settings → Allgemein → Darstellung: theme, density and the default mail view. -->
<script lang="ts">
  import Sun from '@lucide/svelte/icons/sun';
  import Moon from '@lucide/svelte/icons/moon';
  import Monitor from '@lucide/svelte/icons/monitor';
  import { app, prefs } from '../../state';
  import type { Density, Theme } from '../../state/prefs.svelte';
  import { t } from '../../i18n/index.svelte';

  const themes: { id: Theme; icon: typeof Sun }[] = [
    { id: 'light', icon: Sun },
    { id: 'dark', icon: Moon },
    { id: 'system', icon: Monitor },
  ];
  const densities: Density[] = ['compact', 'normal', 'comfortable'];
</script>

<section class="card" id="set-look" aria-labelledby="set-look-h">
  <h2 id="set-look-h">{t('settings.appearance')}</h2>
  <p class="intro">{t('settings.pages.general.appearanceIntro')}</p>
  <div class="field">
    <span class="label" id="lbl-theme">{t('settings.theme')}</span>
    <div class="seg" role="radiogroup" aria-labelledby="lbl-theme">
      {#each themes as th (th.id)}
        {@const Icon = th.icon}
        <button role="radio" aria-checked={prefs.theme === th.id} onclick={() => ((prefs.theme = th.id), prefs.save())}><Icon size={14} /> {t(`settings.themes.${th.id}`)}</button>
      {/each}
    </div>
  </div>
  <div class="field">
    <span class="label" id="lbl-density">{t('settings.density')}</span>
    <div class="seg" role="radiogroup" aria-labelledby="lbl-density">
      {#each densities as d (d)}
        <button role="radio" aria-checked={prefs.density === d} onclick={() => ((prefs.density = d), prefs.save())}>{t(`settings.densities.${d}`)}</button>
      {/each}
    </div>
  </div>
  <div class="field">
    <span class="label" id="lbl-view">{t('settings.defaultView')}</span>
    <div class="seg" role="radiogroup" aria-labelledby="lbl-view">
      <button role="radio" aria-checked={prefs.viewMode === 'classic'} onclick={() => app.setViewMode('classic')}>{t('mail.view.classic')}</button>
      <button role="radio" aria-checked={prefs.viewMode === 'hybrid'} onclick={() => app.setViewMode('hybrid')}>{t('mail.view.hybrid')}</button>
      <button role="radio" aria-checked={prefs.viewMode === 'chat'} onclick={() => app.setViewMode('chat')}>{t('mail.view.chat')}</button>
    </div>
  </div>
</section>

<style>
  .field {
    margin-bottom: 14px;
  }
  .field:last-child {
    margin-bottom: 0;
  }
</style>
