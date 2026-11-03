<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- Icon of a tag (small fixed set, stored by name). -->
<script lang="ts" module>
  import TagI from '@lucide/svelte/icons/tag';
  import Briefcase from '@lucide/svelte/icons/briefcase';
  import House from '@lucide/svelte/icons/house';
  import Heart from '@lucide/svelte/icons/heart';
  import Flame from '@lucide/svelte/icons/flame';
  import Receipt from '@lucide/svelte/icons/receipt';
  import Hammer from '@lucide/svelte/icons/hammer';
  import Star from '@lucide/svelte/icons/star';
  import Users from '@lucide/svelte/icons/users';
  import Flag from '@lucide/svelte/icons/flag';
  import Bookmark from '@lucide/svelte/icons/bookmark';
  import Plane from '@lucide/svelte/icons/plane';

  export const TAG_ICONS = { tag: TagI, briefcase: Briefcase, house: House, heart: Heart, flame: Flame, receipt: Receipt, hammer: Hammer, star: Star, users: Users, flag: Flag, bookmark: Bookmark, plane: Plane } as const;
  export const TAG_COLORS = ['#2563eb', '#0ea5e9', '#7c3aed', '#db2777', '#dc2626', '#d97706', '#a16207', '#16a34a', '#0d9488', '#64748b'];
</script>

<script lang="ts">
  interface Props {
    icon?: string;
    size?: number;
    color?: string;
  }
  let { icon, size = 14, color }: Props = $props();
  const Icon = $derived(TAG_ICONS[(icon ?? 'tag') as keyof typeof TAG_ICONS] ?? TagI);
</script>

<Icon {size} {color} aria-hidden="true" />
