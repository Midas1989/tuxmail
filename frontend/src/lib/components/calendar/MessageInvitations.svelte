<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts" module>
  import type { Invitation } from '../../types';

  // One request per message and session (bubbles of a conversation ask
  // for the same mails repeatedly); answers update the cache.
  const cache = new Map<number, Promise<Invitation[]>>();

  export function invalidateInvitations(messageId?: number): void {
    if (messageId === undefined) cache.clear();
    else cache.delete(messageId);
  }
</script>

<script lang="ts">
  import { app } from '../../state';
  import InvitationCard from './InvitationCard.svelte';

  interface Props {
    messageId: number;
    compact?: boolean;
  }
  let { messageId, compact = false }: Props = $props();
  let list = $state<Invitation[]>([]);

  $effect(() => {
    const id = messageId;
    let p = cache.get(id);
    if (!p) {
      p = app.api.getMessageInvitations(id).catch(() => [] as Invitation[]);
      cache.set(id, p);
    }
    let alive = true;
    void p.then((l) => alive && (list = l));
    return () => {
      alive = false;
    };
  });

  $effect(() => {
    return app.api.subscribe((e) => {
      if (e.type === 'calendar.invitation' && e.messageId === messageId) {
        cache.delete(messageId);
        void app.api.getMessageInvitations(messageId).then((l) => (list = l));
      }
    });
  });

  function changed(inv: Invitation) {
    list = list.map((x) => (x.id === inv.id ? inv : x));
    cache.set(messageId, Promise.resolve(list));
  }
</script>

{#if list.length}
  <div class="mi" class:compact>
    {#each list as inv (inv.id)}
      <InvitationCard invitation={inv} {compact} onchange={changed} />
    {/each}
  </div>
{/if}

<style>
  .mi {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 4px 0 12px;
  }
  .mi.compact {
    margin: 6px 0 2px;
  }
</style>
