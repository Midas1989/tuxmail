<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Fresh re-authentication of the administrator before a privileged action
  (reset password / two-factor of a user: M-2). The same form as for the
  self-service security changes (StepUpForm); `run` performs the action with
  the proof and closes the dialog itself on success, errors show up here.
-->
<script lang="ts">
  import Modal from '../common/Modal.svelte';
  import StepUpForm from '../security/StepUpForm.svelte';
  import { t } from '../../i18n/index.svelte';
  import type { AdminMe } from '../../api/admin';
  import type { Reauth } from '../../api/security';
  import { adminErrorMessage } from '../../admin/errors';

  interface Props {
    me: AdminMe;
    title: string;
    danger?: boolean;
    run: (reauth: Reauth) => Promise<void>;
    onclose: () => void;
  }
  let { me, title, danger = false, run, onclose }: Props = $props();

  let busy = $state(false);
  let error = $state<string | null>(null);

  async function submit(reauth: Reauth) {
    busy = true;
    error = null;
    try {
      await run(reauth);
    } catch (err) {
      error = adminErrorMessage(err, t);
    } finally {
      busy = false;
    }
  }
</script>

<Modal {title} {onclose} width="480px">
  <StepUpForm hasPassword={me.hasPassword ?? true} hasSecondFactor={me.totpEnabled ?? false} submitLabel={title} {danger} {busy} {error} onsubmit={(r) => void submit(r)} oncancel={onclose} />
</Modal>
