<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
<script lang="ts" module>
  export function escapeHtml(s: string): string {
    return s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
  }

  /** Plain text -> HTML with quote levels and links (no raw HTML from the mail survives). */
  export function plainToHtml(text: string): string {
    return text
      .split('\n')
      .map((line) => {
        const esc = escapeHtml(line).replace(/(https?:\/\/[^\s<]+)/g, '<a href="$1">$1</a>');
        const m = /^(&gt;\s?)+/.exec(esc);
        if (m) return `<span class="q">${esc}</span>`;
        return esc;
      })
      .join('\n');
  }

  /**
   * Image sources allowed after the user loaded remote content: the origin of
   * the image proxy when the HTML uses it (signed /api/proxy/image links, the
   * sender never sees the reader), otherwise https: for directly restored
   * images (demo transport without a proxy).
   */
  export function remoteImageSources(html: string): string {
    const origins = new Set<string>();
    for (const m of html.matchAll(/src="(https?:\/\/[^/"]+)?\/api\/proxy\/image/g)) {
      origins.add(m[1] ?? (typeof location !== 'undefined' && location.origin !== 'null' ? location.origin : "'self'"));
    }
    return origins.size ? [...origins].join(' ') : 'https:';
  }

  /**
   * Build the srcdoc for the sandboxed iframe. The iframe gets sandbox="" (no
   * scripts, no same-origin, no forms, no popups) and its own CSP; remote
   * resources stay blocked unless the user explicitly allows them.
   */
  export function buildSrcdoc(opts: { html?: string; text?: string; allowRemote: boolean; dark: boolean }): string {
    const img = opts.allowRemote ? `data: cid: ${remoteImageSources(opts.html ?? '')}` : 'data: cid:';
    const csp = `default-src 'none'; img-src ${img}; style-src 'unsafe-inline'; font-src data:; media-src 'none'; script-src 'none'; object-src 'none'; frame-src 'none'; child-src 'none'; base-uri 'none'; form-action 'none'`;
    const plain = !opts.html;
    const dark = plain && opts.dark;
    const css = `
      html{color-scheme:${dark ? 'dark' : 'light'}}
      body{margin:0;padding:20px 24px;font:14px/1.55 system-ui,-apple-system,'Segoe UI',Roboto,Ubuntu,'Noto Sans',Arial,sans-serif;
        color:${dark ? '#e7e9ee' : '#1f2328'};background:${dark ? '#161920' : '#ffffff'};overflow-wrap:anywhere}
      img{max-width:100%;height:auto}
      img[data-tracker]{display:none}
      img[data-remote-src]{width:24px!important;height:24px!important;outline:1px dashed ${dark ? '#353b47' : '#d0d7de'};vertical-align:middle}
      a{color:${dark ? '#93b8ff' : '#1d4ed8'}}
      pre.plain{white-space:pre-wrap;font:inherit;margin:0}
      .q{color:${dark ? '#8b93a1' : '#6b7280'}}
      blockquote{margin:0 0 0 4px;padding-left:12px;border-left:3px solid ${dark ? '#353b47' : '#d0d7de'};color:${dark ? '#a2a9b6' : '#57606a'}}
      table{max-width:100%}`;
    // Remote images are only ever loaded through the proxy URLs the core
    // put into the HTML (Api.loadRemoteContent); blocked images are never
    // restored to their direct URLs here (audit B-05: IP/tracking leak).
    const body = plain ? `<pre class="plain">${plainToHtml(opts.text ?? '')}</pre>` : opts.html!;
    return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><meta name="referrer" content="no-referrer"><base target="_blank"><style>${css}</style></head><body>${body}</body></html>`;
  }
</script>

<script lang="ts">
  interface Props {
    html?: string;
    text?: string;
    allowRemote?: boolean;
    dark?: boolean;
    title: string;
    /**
     * Grow with the content (full mail inside a chat bubble). Needs
     * sandbox="allow-same-origin" so that the app can measure the document;
     * scripts stay disabled (no allow-scripts, CSP default-src 'none').
     */
    autoHeight?: boolean;
    maxHeight?: number;
  }
  let { html, text, allowRemote = false, dark = false, title, autoHeight = false, maxHeight = 4000 }: Props = $props();
  const srcdoc = $derived(buildSrcdoc({ html, text, allowRemote, dark }));
  let frame = $state<HTMLIFrameElement>();
  let height = $state(80);
  let ro: ResizeObserver | null = null;

  function measure() {
    try {
      const d = frame?.contentDocument;
      if (!d?.documentElement) return;
      height = Math.min(Math.max(d.documentElement.scrollHeight, 40), maxHeight);
    } catch {
      /* not measurable */
    }
  }
  function onload() {
    if (!autoHeight) return;
    measure();
    try {
      const d = frame?.contentDocument;
      ro?.disconnect();
      if (d?.body && typeof ResizeObserver !== 'undefined') {
        ro = new ResizeObserver(measure);
        ro.observe(d.body);
      }
      d?.addEventListener('load', measure, true); // images
    } catch {
      /* ignore */
    }
  }
  $effect(() => () => ro?.disconnect());
</script>

{#if autoHeight}
  <iframe class="frame auto" bind:this={frame} sandbox="allow-same-origin" {srcdoc} {title} referrerpolicy="no-referrer" {onload} style:height="{height}px"></iframe>
{:else}
  <!-- sandbox="" = maximum restrictions; the mail can neither run scripts nor reach the app. -->
  <iframe class="frame" sandbox="" {srcdoc} {title} referrerpolicy="no-referrer"></iframe>
{/if}

<style>
  .frame {
    display: block;
    width: 100%;
    height: 100%;
    border: 0;
    background: transparent;
  }
</style>
