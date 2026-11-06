<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Blocking dialog "Set up two-factor authentication": shown while the server
  reports mfaSetupRequired (administrator without TOTP while
  auth.totp_required_for_admins is on), i.e. from /api/auth/me or any 403
  mfa_setup_required. It cannot be dismissed; the only alternative to setting
  it up is signing out. After the set-up the page state is reloaded.
-->
<script lang="ts">
  import ShieldAlert from '@lucide/svelte/icons/shield-alert';
  import LogOut from '@lucide/svelte/icons/log-out';
  import { app, security } from '../../state';
  import type { Api } from '../../api/api';
  import { t } from '../../i18n/index.svelte';
  import { signOutWeb } from '../../session';
  import Modal from '../common/Modal.svelte';
  import TotpEnrollment from './TotpEnrollment.svelte';

  interface Props {
    /** Defaults to the application API. */
    api?: Api;
    /** Defaults to the page-wide security state (reloads the page when done). */
    onfinished?: () => void;
    /** Defaults to the web sign-out (end the session, clear local data, reload). */
    onsignout?: () => Promise<void>;
  }
  let { api: apiProp, onfinished, onsignout }: Props = $props();
  const api = $derived(apiProp ?? app.api);

  let signOutError = $state<string | null>(null);
  let busy = $state(false);

  async function signOut() {
    busy = true;
    signOutError = null;
    try {
      await (onsignout ? onsignout() : signOutWeb({ api }));
    } catch (err) {
      signOutError = t('settings.pages.session.failed', { error: err instanceof Error ? err.message : String(err) });
    } finally {
      busy = false;
    }
  }
</script>

<!-- Not closable: Escape and clicks on the backdrop do nothing. -->
<Modal title={t('security.mfaDialog.title')} onclose={() => {}} closable={false} width="520px">
  <div class="mfa" data-testid="mfa-setup-dialog">
    <p class="lead"><ShieldAlert size={18} aria-hidden="true" /> <span>{t('security.mfaDialog.lead')}</span></p>
    <TotpEnrollment {api} onfinished={() => (onfinished ? onfinished() : security.finished())} />
    {#if signOutError}<p class="error" role="alert">{signOutError}</p>{/if}
  </div>
  {#snippet footer()}
    <span class="alt">{t('security.mfaDialog.alternative')}</span>
    <button type="button" class="btn" onclick={() => void signOut()} disabled={busy}><LogOut size={14} /> {t('settings.pages.session.signOut')}</button>
  {/snippet}
</Modal>

<style>
  .mfa {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .lead {
    display: flex;
    gap: 8px;
    align-items: flex-start;
    margin: 0;
    font-size: 13.5px;
  }
  .lead :global(svg) {
    flex: none;
    margin-top: 1px;
    color: var(--tm-warn);
  }
  .error {
    margin: 0;
    color: var(--tm-danger);
    font-size: 13px;
  }
  .alt {
    margin-right: auto;
    align-self: center;
    font-size: 12.5px;
    color: var(--tm-muted);
  }
</style>
