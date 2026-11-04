<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Settings → Info & Diagnose: about TuxMail (logo, version, licence,
  copyright) and the licence texts (LICENSE, NOTICE, THIRD_PARTY_NOTICES.md
  from the repository root, loaded on demand as a separate chunk).
-->
<script lang="ts">
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import FileText from '@lucide/svelte/icons/file-text';
  import { app } from '../../state';
  import { t } from '../../i18n/index.svelte';
  import { openExternal } from '../../platform';
  import logoUrl from '$branding/tuxmail-logo.png';
  import buildVersion from '../../../../../VERSION?raw';

  interface Props {
    part: 'about' | 'licenses';
  }
  let { part }: Props = $props();

  const SOURCE_URL = 'https://github.com/Midas1989/Mailclient';

  let version = $state(buildVersion.trim());
  $effect(() => {
    if (part !== 'about') return;
    void app.api
      .diagnostics()
      .then((d) => d.version && (version = d.version))
      .catch(() => {});
  });

  let shown = $state<'license' | 'notices' | null>(null);
  let text = $state('');

  async function show(which: 'license' | 'notices') {
    if (shown === which) {
      shown = null;
      return;
    }
    shown = which;
    text = t('settings.pages.about.loading');
    try {
      if (which === 'license') {
        const [notice, license] = await Promise.all([import('../../../../../NOTICE?raw'), import('../../../../../LICENSE?raw')]);
        text = notice.default + '\n\n' + license.default;
      } else {
        text = (await import('../../../../../THIRD_PARTY_NOTICES.md?raw')).default;
      }
    } catch (err) {
      text = err instanceof Error ? err.message : String(err);
    }
  }
</script>

{#if part === 'about'}
  <section class="card" id="set-about" aria-labelledby="set-about-h">
    <h2 id="set-about-h">{t('settings.about')}</h2>
    <div class="about">
      <img class="logo" src={logoUrl} alt="TuxMail" width="96" height="96" />
      <div>
        <p class="name">TuxMail</p>
        <p class="ver" data-testid="about-version">{t('settings.pages.about.version', { version })}</p>
        <p class="txt">{t('settings.aboutText')}</p>
        <p class="txt">{t('settings.pages.about.copyright')}</p>
        <p class="txt">{t('settings.transport', { kind: app.api.kind })}</p>
        <button class="btn btn-ghost" onclick={() => openExternal(SOURCE_URL)}><ExternalLink size={14} /> {t('settings.pages.about.source')}</button>
      </div>
    </div>
  </section>
{:else}
  <section class="card" id="set-licenses" aria-labelledby="set-licenses-h">
    <h2 id="set-licenses-h">{t('settings.pages.about.sections.licenses')}</h2>
    <p class="intro">{t('settings.pages.about.licensesIntro')}</p>
    <div class="btns">
      <button class="btn" aria-expanded={shown === 'license'} aria-controls="set-license-text" onclick={() => show('license')}><FileText size={14} /> {t('settings.pages.about.showLicense')}</button>
      <button class="btn" aria-expanded={shown === 'notices'} aria-controls="set-license-text" onclick={() => show('notices')}><FileText size={14} /> {t('settings.pages.about.showNotices')}</button>
    </div>
    {#if shown}
      <textarea id="set-license-text" class="license" readonly rows="18" aria-label={shown === 'license' ? t('settings.pages.about.showLicense') : t('settings.pages.about.showNotices')} value={text}></textarea>
    {/if}
  </section>
{/if}

<style>
  .about {
    display: flex;
    gap: 18px;
    align-items: flex-start;
  }
  .logo {
    width: 96px;
    height: auto;
    border-radius: 16px;
    flex: none;
  }
  .name {
    margin: 0;
    font-size: 17px;
    font-weight: 700;
  }
  .ver {
    margin: 0 0 8px;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .txt {
    margin: 0 0 8px;
    font-size: 13px;
    color: var(--tm-muted);
  }
  .btns {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .license {
    margin: 12px 0 0;
    height: 420px;
    display: block;
    overflow: auto;
    padding: 12px 14px;
    border: 1px solid var(--tm-line);
    border-radius: var(--tm-radius);
    background: var(--tm-surface-2);
    font-family: var(--tm-mono);
    font-size: 12px;
    color: inherit;
    width: 100%;
    resize: vertical;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  @media (max-width: 639px) {
    .about {
      flex-direction: column;
    }
  }
</style>
