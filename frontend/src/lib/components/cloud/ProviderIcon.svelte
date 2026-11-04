<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts">
  import Cloud from '@lucide/svelte/icons/cloud';
  import Lock from '@lucide/svelte/icons/lock';
  import HardDrive from '@lucide/svelte/icons/hard-drive';
  import type { CloudAccountProvider } from '../../types';

  interface Props {
    provider: CloudAccountProvider;
    size?: number;
  }
  let { provider, size = 16 }: Props = $props();
  const Icon = $derived(provider === 'vaultwarden' ? Lock : provider === 'webdav' ? HardDrive : Cloud);
</script>

<span class="pi pi-{provider}" aria-hidden="true"><Icon {size} /></span>

<style>
  .pi {
    display: inline-flex;
    flex: none;
  }
  .pi-nextcloud {
    color: #0082c9;
  }
  .pi-opencloud {
    color: #e2a01f;
  }
  .pi-vaultwarden {
    color: #175ddc;
  }
  .pi-webdav {
    color: var(--tm-muted);
  }
</style>
