// SPDX-License-Identifier: AGPL-3.0-or-later
import { describe, expect, it } from 'vitest';
import { HttpTransport } from './http';
import { MockTransport } from './mock';

async function finish(api: MockTransport, id: string) {
  for (let i = 0; i < 200; i++) {
    const j = await api.getIOJob(id);
    if (j.status !== 'running') return j;
    await new Promise((r) => setTimeout(r, 20));
  }
  throw new Error('job did not finish');
}

describe('import/export/backup (mock)', () => {
  it('runs an import job to completion with a summary', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const up = await api.uploadImportFile(new Blob(['From x']), 'archive.mbox');
    const preview = await api.inspectImport({ format: 'mbox', uploadToken: up.token });
    expect(preview.items).toBeGreaterThan(0);
    const job = await api.startImport({
      source: { format: 'mbox', uploadToken: up.token },
      target: { kind: 'local' },
      options: { keepStructure: true, dedupe: true, keepFlags: true, keepDates: true, tbAccounts: false, tbMail: false, tbAddressBooks: false },
    });
    const done = await finish(api, job.id);
    expect(done.status).toBe('done');
    expect(done.summary?.messages).toBeGreaterThan(0);
  });

  it('suggests a CSV mapping and can cancel jobs', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const p = await api.inspectImport({ format: 'csv', path: 'contacts.csv' });
    expect(p.preset).toBe('outlook');
    expect(p.mapping?.length).toBe(p.header?.length);
    const job = await api.startExport({ format: 'mbox', selection: { query: 'x' } });
    await api.cancelIOJob(job.id);
    expect((await api.getIOJob(job.id)).status).toBe('canceled');
  });

  it('inspects backups and rejects a wrong passphrase', async () => {
    const api = new MockTransport({ latencyMs: 0 });
    const src = { uploadToken: 'iou_x' };
    expect((await api.inspectBackup(src)).manifest).toBeNull();
    await expect(api.inspectBackup(src, 'wrong')).rejects.toMatchObject({ code: 'invalid_password' });
    const ins = await api.inspectBackup(src, 'correct horse');
    expect(ins.manifest?.encrypted).toBe(true);
    const s = await api.saveBackupSchedule({ id: '', name: 'n', enabled: true, frequency: 'weekly', hour: 3, minute: 0, weekday: 0, scope: { mode: 'full' }, target: { kind: 'local', path: '/b' }, incremental: false, keepDaily: 7, keepWeekly: 4, passphrase: 'x' });
    expect(s.passphraseRef).toBeTruthy();
    expect((await api.listBackupSchedules()).some((x) => x.id === s.id)).toBe(true);
  });

  it('http transport uses the documented routes', async () => {
    const calls: string[] = [];
    const api = new HttpTransport({
      fetchImpl: (async (url: string, init?: RequestInit) => {
        calls.push(`${init?.method} ${url}`);
        return new Response(JSON.stringify({ id: 'j', status: 'running' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
      }) as typeof fetch,
    });
    await api.startImport({ source: { format: 'vcf', uploadToken: 't' }, target: { kind: 'local' }, options: { keepStructure: true, dedupe: true, keepFlags: true, keepDates: true, tbAccounts: false, tbMail: false, tbAddressBooks: false } });
    await api.saveBackupSchedule({ id: 'bk_1', name: 'n', enabled: true, frequency: 'daily', hour: 1, minute: 0, weekday: 0, scope: { mode: 'config' }, target: { kind: 'local' }, incremental: false, keepDaily: 7, keepWeekly: 4 });
    await api.inspectBackup({ uploadToken: 't' }, 'pw');
    expect(calls).toEqual(['POST /api/io/import', 'PUT /api/backup/schedules/bk_1', 'POST /api/backup/inspect']);
    expect(api.ioDownloadUrl('iod_1')).toBe('/api/io/downloads/iod_1');
  });
});
