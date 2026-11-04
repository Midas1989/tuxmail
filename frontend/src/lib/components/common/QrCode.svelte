<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!-- QR code as inline SVG (always black on white so phones can scan it in dark mode). -->
<script lang="ts">
  import { qrMatrix } from '../../qr';

  interface Props {
    data: string;
    size?: number;
    label: string;
  }
  let { data, size = 240, label }: Props = $props();
  const m = $derived(qrMatrix(data));
</script>

<svg class="qr" width={size} height={size} viewBox="0 0 {m.viewBox} {m.viewBox}" role="img" aria-label={label} shape-rendering="crispEdges">
  <rect width={m.viewBox} height={m.viewBox} fill="#fff" />
  <path d={m.path} fill="#000" />
</svg>

<style>
  .qr {
    display: block;
    border-radius: 10px;
    box-shadow: 0 0 0 1px var(--tm-line);
  }
</style>
