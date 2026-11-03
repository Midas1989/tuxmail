// SPDX-License-Identifier: AGPL-3.0-or-later
import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { defineConfig, type Plugin } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';

/**
 * Emits dist/sw.js from src/pwa/sw.js with the precache manifest (all
 * emitted bundle files + public/ files) and a content-based build id.
 * Hand-written instead of vite-plugin-pwa to avoid the workbox dependency tree.
 */
function serviceWorker(): Plugin {
  const publicDir = join(import.meta.dirname, 'public');
  const listPublic = (dir: string): string[] =>
    readdirSync(dir).flatMap((f) => {
      const p = join(dir, f);
      return statSync(p).isDirectory() ? listPublic(p) : [relative(publicDir, p).split('\\').join('/')];
    });
  return {
    name: 'tuxmail-sw',
    apply: 'build',
    generateBundle(_opts, bundle) {
      const files = ['./', ...Object.keys(bundle).filter((f) => !f.endsWith('.map')), ...listPublic(publicDir)]
        .filter((f) => f !== 'sw.js')
        .map((f) => (f.startsWith('./') ? f : './' + f));
      const unique = [...new Set(files)].sort();
      const id = createHash('sha256').update(unique.join('\n')).digest('hex').slice(0, 12);
      const src = readFileSync(join(import.meta.dirname, 'src/pwa/sw.js'), 'utf8')
        .replace("'__BUILD_ID__'", JSON.stringify(id))
        .replace('__PRECACHE_MANIFEST__', JSON.stringify(unique));
      this.emitFile({ type: 'asset', fileName: 'sw.js', source: src });
    },
  };
}

// The same build is embedded into the Wails desktop shell and served by
// `tuxmail serve` for the web client, so asset paths must be relative.
export default defineConfig({
  base: './',
  plugins: [tailwindcss(), svelte(), serviceWorker()],
  server: {
    port: 5173,
    proxy: {
      // `npm run dev` + `?api=http` talks to a locally running `tuxmail serve`.
      '/api': { target: 'http://127.0.0.1:8080', ws: true },
    },
  },
  build: {
    target: 'es2022',
    chunkSizeWarningLimit: 800,
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
});
