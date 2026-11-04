// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Cloud accounts and the global "Save to cloud" dialog (KONZEPT 12).

import type { Api } from '../api';
import type { Attachment, AttachmentDelivery, CloudAccount, CloudProvider } from '../types';

/** Where an attachment to save comes from. */
export interface CloudSaveSource {
  filename: string;
  size?: number;
  messageId?: number;
  index?: number;
  uploadToken?: string;
}

/** Mail and chat attachments are served as /api/messages/{id}/attachments/{index}. */
export function attachmentSource(a: Attachment): CloudSaveSource | null {
  const m = a.url?.match(/\/api\/messages\/(\d+)\/attachments\/(\d+)/);
  if (m) return { filename: a.filename, size: a.size, messageId: Number(m[1]), index: Number(m[2]) };
  return null;
}

/** Breadcrumb segments of a path ("/a/b" -> ["/", "/a", "/a/b"]). */
export function breadcrumbs(path: string): { path: string; name: string }[] {
  const parts = path.split('/').filter(Boolean);
  const out = [{ path: '/', name: '' }];
  let cur = '';
  for (const p of parts) {
    cur += '/' + p;
    out.push({ path: cur, name: p });
  }
  return out;
}

export function joinPath(dir: string, name: string): string {
  return (dir.replace(/\/+$/, '') + '/' + name.replace(/^\/+/, '')).replace(/\/+/g, '/') || '/';
}

export function parentPath(path: string): string {
  const i = path.replace(/\/+$/, '').lastIndexOf('/');
  return i <= 0 ? '/' : path.slice(0, i);
}

export class CloudState {
  accounts = $state<CloudAccount[]>([]);
  loaded = $state(false);
  /** Open "Save to cloud" dialog. */
  saving = $state<CloudSaveSource | null>(null);

  constructor(private readonly api: Api) {}

  async load(): Promise<void> {
    try {
      this.accounts = await this.api.listCloudAccounts();
    } catch {
      this.accounts = [];
    }
    this.loaded = true;
  }

  async ensureLoaded(): Promise<void> {
    if (!this.loaded) await this.load();
  }

  get storageAccounts(): CloudAccount[] {
    return this.accounts.filter((a) => a.capabilities.files);
  }

  account(id: number): CloudAccount | undefined {
    return this.accounts.find((a) => a.id === id);
  }

  /** Composer delivery modes that have an account. */
  hasDelivery(d: AttachmentDelivery): boolean {
    return d === 'attach' || this.accounts.some((a) => a.provider === (d as CloudProvider));
  }

  openSave(src: CloudSaveSource): void {
    void this.ensureLoaded();
    this.saving = src;
  }
}
