// SPDX-License-Identifier: AGPL-3.0-or-later
import { createHash } from 'node:crypto';
import { copyFileSync, existsSync, mkdirSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
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

/**
 * Official brand icons live in assets/branding/icons (repo root). When they
 * exist they replace the icons generated into public/icons (fallback, see
 * scripts/generate-icons.mjs) - in dev via middleware, in builds by copying
 * over the output after Vite copied public/.
 */
const BRAND_ICONS: Record<string, string> = {
  'icons/icon-32.png': 'icon-32.png',
  'icons/icon-64.png': 'icon-64.png',
  'icons/icon-192.png': 'icon-192.png',
  'icons/icon-512.png': 'icon-512.png',
  'icons/icon-maskable-512.png': 'icon-maskable-512.png',
  'icons/apple-touch-icon.png': 'icon-180.png',
  'favicon.ico': 'favicon.ico',
};

function branding(): Plugin {
  const dir = join(import.meta.dirname, '../assets/branding/icons');
  const available = () => Object.entries(BRAND_ICONS).filter(([, src]) => existsSync(join(dir, src)));
  let outDir = 'dist';
  return {
    name: 'tuxmail-branding',
    configResolved(cfg) {
      outDir = cfg.build.outDir;
    },
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const path = (req.url ?? '').split('?')[0].replace(/^\//, '');
        const hit = available().find(([target]) => target === path);
        if (!hit) return next();
        res.setHeader('Content-Type', path.endsWith('.ico') ? 'image/x-icon' : 'image/png');
        res.end(readFileSync(join(dir, hit[1])));
      });
    },
    writeBundle() {
      for (const [target, src] of available()) {
        const dest = join(import.meta.dirname, outDir, target);
        mkdirSync(dirname(dest), { recursive: true });
        copyFileSync(join(dir, src), dest);
      }
    },
  };
}

// The same build is embedded into the Wails desktop shell and served by
// `tuxmail serve` for the web client, so asset paths must be relative.
const root = import.meta.dirname;

export default defineConfig({
  base: './',
  plugins: [tailwindcss(), svelte(), serviceWorker(), branding()],
  resolve: {
    alias: {
      // Single source of truth for translations and branding at the repo root.
      $locales: join(root, '../locales'),
      $branding: join(root, '../assets/branding'),
    },
  },
  server: {
    port: 5173,
    fs: { allow: [join(root, '..')] },
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
