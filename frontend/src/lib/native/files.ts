// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// "Anhänge mit System-Apps öffnen" (KONZEPT 15.9): write a blob into the app
// cache (@capacitor/filesystem) and hand it to the Android share sheet
// (@capacitor/share, FileProvider). UI code that only has a Blob dispatches
//   window.dispatchEvent(new CustomEvent('tuxmail:open-file', { detail: { blob, name } }))
// which works in the app and is a no-op elsewhere.

import { Filesystem, Share } from './plugins';

export const OPEN_FILE_EVENT = 'tuxmail:open-file';

/** Safe cache file name (no directories, no hidden files). */
export function cacheFileName(name: string): string {
  const base = name.split(/[\\/]/).pop() ?? '';
  const clean = base.replace(/[\u0000-\u001f<>:"|?*]/g, '_').replace(/^\.+/, '').trim();
  return (clean || 'file').slice(0, 120);
}

async function toBase64(blob: Blob): Promise<string> {
  const buf = new Uint8Array(await blob.arrayBuffer());
  let bin = '';
  for (let i = 0; i < buf.length; i += 0x8000) bin += String.fromCharCode(...buf.subarray(i, i + 0x8000));
  return btoa(bin);
}

export async function openWithSystem(blob: Blob, name: string): Promise<void> {
  const path = `outgoing/${Date.now().toString(36)}/${cacheFileName(name)}`;
  const { uri } = await Filesystem.writeFile({ path, data: await toBase64(blob), directory: 'CACHE', recursive: true });
  await Share.share({ files: [uri], dialogTitle: name });
}

export function installOpenFileHandler(target: EventTarget = window): () => void {
  const handler = (e: Event) => {
    const d = (e as CustomEvent<{ blob?: Blob; name?: string }>).detail;
    if (d?.blob instanceof Blob) {
      e.preventDefault();
      void openWithSystem(d.blob, d.name ?? 'file').catch((err) => console.warn('open with system app failed', err));
    }
  };
  target.addEventListener(OPEN_FILE_EVENT, handler);
  return () => target.removeEventListener(OPEN_FILE_EVENT, handler);
}
