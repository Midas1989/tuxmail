<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<!--
  Editor of one signature (Settings → Konten → Signaturen). Mount point for
  the rich signature editor (HTML, images, links); for now the plain-text
  field. Reports changes through onchange; SignatureSettings saves them.
-->
<script lang="ts">
  import { t } from '../../i18n/index.svelte';
  import type { Signature } from '../../types';

  interface Props {
    signature: Signature;
    onchange: (next: Signature) => void;
  }
  let { signature, onchange }: Props = $props();
</script>

<div class="sig-editor" data-signature-editor={signature.id}>
  <textarea class="input text" rows="4" value={signature.plain} aria-label={t('settings.signatures.text')} onchange={(e) => onchange({ ...signature, plain: (e.currentTarget as HTMLTextAreaElement).value })}></textarea>
</div>

<style>
  .sig-editor {
    margin-top: 4px;
  }
  .text {
    width: 100%;
    height: auto;
    padding: 6px 10px;
    resize: vertical;
    font-family: var(--tm-mono);
    font-size: 12.5px;
  }
</style>
