<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Global cloud dialogs (mounted once in App.svelte). -->
<script lang="ts">
  import { cloud } from '../../state';
  import CloudSaveDialog from './CloudSaveDialog.svelte';

  // Delivery options, "Save to cloud" and the picker need the accounts.
  $effect(() => {
    void cloud.ensureLoaded();
  });
</script>

{#if cloud.saving}
  <CloudSaveDialog source={cloud.saving} onclose={() => (cloud.saving = null)} />
{/if}
