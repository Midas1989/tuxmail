<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- "Create online meeting" in the event dialog: Jitsi, Nextcloud Talk or Teams (KONZEPT 9.1). -->
<script lang="ts">
  import Video from '@lucide/svelte/icons/video';
  import { app, comfort } from '../../state';
  import { i18n, t } from '../../i18n/index.svelte';
  import type { MeetingLink, MeetingProvider } from '../../api/comfort';

  interface Props {
    title: string;
    participants: string[];
    start?: string;
    end?: string;
    onlink: (l: MeetingLink) => void;
    /** Used when the backend cannot create a link (e.g. offline). */
    fallback?: () => string;
  }
  let { title, participants, start, end, onlink, fallback }: Props = $props();

  let provider = $state<MeetingProvider>('jitsi');
  let busy = $state(false);
  $effect(() => {
    void comfort.loadSettings().then((s) => s && (provider = s.meeting.defaultProvider || 'jitsi'));
  });

  async function create() {
    busy = true;
    try {
      onlink(await comfort.api.createMeeting({ provider, title, participants, start, end, lang: i18n.locale }));
    } catch (e) {
      if (provider === 'jitsi' && fallback) {
        const url = fallback();
        onlink({ provider, url, label: 'Jitsi Meet', description: t('comfort.meeting.join', { provider: 'Jitsi Meet' }) + ': ' + url, conference: '' });
      } else app.toast(t('comfort.meeting.failed', { error: e instanceof Error ? e.message : String(e) }), undefined, 'error');
    } finally {
      busy = false;
    }
  }
</script>

<span class="mlb">
  <label class="sr-only" for="meeting-provider">{t('comfort.meeting.providerLabel')}</label>
  <select id="meeting-provider" class="input" bind:value={provider}>
    <option value="jitsi">{t('comfort.meeting.provider.jitsi')}</option>
    <option value="nextcloud_talk">{t('comfort.meeting.provider.nextcloud_talk')}</option>
    <option value="teams">{t('comfort.meeting.provider.teams')}</option>
  </select>
  <button type="button" class="btn" disabled={busy} onclick={create}><Video size={15} /> {t('comfort.meeting.create')}</button>
</span>

<style>
  .mlb {
    display: inline-flex;
    gap: 6px;
  }
  .mlb .input {
    width: auto;
  }
</style>
