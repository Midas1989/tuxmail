// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Mock import/export/backup backend for the MockTransport (demo, tests,
// screenshots). Jobs advance on a timer; nothing is really written.

import { ApiError } from './api';
import type {
  Account,
  ApiEvent,
  BackupArchive,
  BackupInspection,
  BackupManifest,
  BackupRequest,
  BackupSchedule,
  BackupScheduleInput,
  BackupSource,
  ExportRequest,
  ImportPreview,
  ImportRequest,
  IOFormats,
  IOJob,
  IOSource,
  IOUpload,
  RestoreRequest,
} from '../types';

const CSV_FIELDS = ['displayName', 'givenName', 'middleName', 'familyName', 'nickname', 'email', 'email2', 'phoneWork', 'phoneHome', 'phoneMobile', 'org', 'title', 'note', 'url', 'birthday'];

interface MockJob {
  job: IOJob;
  timer: ReturnType<typeof setInterval> | null;
}

export class MockIO {
  private jobs = new Map<string, MockJob>();
  private uploads = new Map<string, { name: string; size: number }>();
  private schedules: BackupSchedule[] = [
    {
      id: 'bk_daily',
      name: 'Nextcloud',
      enabled: true,
      frequency: 'daily',
      hour: 2,
      minute: 30,
      weekday: 0,
      scope: { mode: 'full' },
      target: { kind: 'webdav', url: 'https://cloud.example.org/remote.php/dav/files/anna/Backups/', username: 'anna' },
      passphraseRef: 'backup:bk_daily:passphrase',
      incremental: true,
      keepDaily: 7,
      keepWeekly: 4,
      lastRun: '2026-11-03T01:30:12Z',
      lastResult: 'tuxmail-backup-2026-11-03-0230-5f1c9a2e.incr-91b3d0aa.txmb',
      nextRun: '2026-11-04T01:30:00Z',
    },
  ];
  private seq = 1;
  /** Steps per second while a job runs (tests set it high). */
  speed = 8;

  constructor(private readonly emit: (e: ApiEvent) => void) {}

  formats(): IOFormats {
    return {
      import: ['eml', 'mbox', 'maildir', 'pst', 'thunderbird', 'ics', 'vcf', 'csv', 'ldif'],
      export: ['eml', 'mbox', 'maildir', 'ics', 'vcf', 'csv', 'ldif'],
      pst: true,
    };
  }

  upload(file: Blob, name: string): IOUpload {
    const token = `iou_mock${this.seq++}`;
    this.uploads.set(token, { name, size: file.size });
    return { token, name, size: file.size };
  }

  private sourceName(s: { path?: string; uploadToken?: string }): string {
    if (s.uploadToken) return this.uploads.get(s.uploadToken)?.name ?? '';
    return s.path ?? '';
  }

  inspect(source: IOSource): ImportPreview {
    if (!source.path && !source.uploadToken) throw new ApiError('no file selected', 400, 'invalid_input');
    const name = this.sourceName(source).toLowerCase();
    if (name.includes('corrupt')) throw new ApiError('corrupt or unsupported input', 400, 'invalid_input');
    switch (source.format) {
      case 'csv':
        return {
          format: 'csv',
          items: 214,
          preset: 'outlook',
          header: ['First Name', 'Last Name', 'Company', 'E-mail Address', 'Mobile Phone', 'Notes', 'Custom 1'],
          mapping: ['givenName', 'familyName', 'org', 'email', 'phoneMobile', 'note', ''],
          sample: [
            ['Anna', 'Berger', 'Berger & Söhne', 'anna@berger.example', '+49 170 1234567', 'Trade fair', ''],
            ['Max', 'Müller', 'Müller GmbH', 'max@mueller.example', '', '', 'VIP'],
          ],
          fields: CSV_FIELDS,
        };
      case 'thunderbird':
        return {
          format: 'thunderbird',
          items: 0,
          profile: {
            dir: source.path ?? 'abcd1234.default-release',
            accounts: [
              { key: 'account1', name: 'Work (IMAP)', incoming: { kind: 'imap', host: 'imap.example.org', port: 993, username: 'anna' }, identities: [{ name: 'Anna Berger', email: 'anna@example.org' }] },
              { key: 'account2', name: 'Local Folders', incoming: { kind: 'none' }, identities: [], mailDir: 'Mail/Local Folders' },
            ],
            addressBooks: [
              { name: 'Personal Address Book', file: 'abook.sqlite', count: 182 },
              { name: 'Collected Addresses', file: 'history.sqlite', count: 931 },
            ],
            tbsync: ['TbSync is installed', 'Nextcloud (dav) cloud.example.org anna'],
            warnings: ['passwords are not imported; enter them again for each account'],
          },
        };
      case 'ics':
        return { format: 'ics', items: 57 };
      case 'vcf':
      case 'ldif':
        return { format: source.format, items: 128 };
      default:
        return { format: source.format, items: 1843 };
    }
  }

  private start(kind: IOJob['kind'], format: string, total: number, finish: (j: IOJob) => void): IOJob {
    const id = `job_mock${this.seq++}`;
    const job: IOJob = { id, kind, format, status: 'running', done: 0, total, skipped: 0, failed: 0, started: new Date().toISOString() };
    const mj: MockJob = { job, timer: null };
    const step = Math.max(1, Math.ceil(total / 12));
    mj.timer = setInterval(() => {
      if (job.status !== 'running') return;
      job.done = Math.min(total, job.done + step);
      if (job.done >= total) {
        if (mj.timer) clearInterval(mj.timer);
        mj.timer = null;
        job.status = 'done';
        job.finished = new Date().toISOString();
        finish(job);
      }
      this.emit({ type: 'io.job', itemId: id, state: job.status });
    }, 1000 / this.speed);
    this.jobs.set(id, mj);
    return job;
  }

  startImport(req: ImportRequest): IOJob {
    if (!req.source?.path && !req.source?.uploadToken && !req.resumeJobId) throw new ApiError('no file selected', 400, 'invalid_input');
    const f = req.source.format;
    const total = f === 'ics' ? 57 : f === 'vcf' || f === 'csv' || f === 'ldif' ? 128 : 1843;
    return this.start('import', f, total, (j) => {
      if (f === 'ics') j.summary = { events: 54, duplicates: 3 };
      else if (f === 'vcf' || f === 'csv' || f === 'ldif') j.summary = { contacts: 126, duplicates: 2 };
      else if (f === 'thunderbird') j.summary = { accounts: 1, messages: 1795, folders: 14, contacts: 182 };
      else j.summary = { messages: 1795, folders: req.options.keepStructure ? 14 : 1 };
      j.skipped = req.options.dedupe ? 46 : 0;
      j.failed = f === 'pst' ? 2 : 0;
      j.problems = j.failed ? [{ item: 'Calendar/Meeting.msg', error: 'appointment items are not converted' }] : [];
      this.emit({ type: 'account.updated', accountId: 0 });
    });
  }

  startExport(req: ExportRequest): IOJob {
    if (req.format === 'pdf') throw new ApiError('export format "pdf" is not available', 501, 'not_supported');
    const total = ['ics'].includes(req.format) ? 57 : ['vcf', 'csv', 'ldif'].includes(req.format) ? 128 : 312;
    return this.start('export', req.format, total, (j) => {
      j.summary = req.format === 'ics' ? { events: total } : ['vcf', 'csv', 'ldif'].includes(req.format) ? { contacts: total } : { messages: total };
      j.download = `iod_mock${this.seq++}`;
      j.downloadName = `tuxmail-export.${req.format === 'eml' || req.format === 'maildir' ? 'zip' : req.format}`;
    });
  }

  jobsList(): IOJob[] {
    return [...this.jobs.values()].map((m) => m.job).sort((a, b) => b.started.localeCompare(a.started));
  }

  job(id: string): IOJob {
    const j = this.jobs.get(id);
    if (!j) throw new ApiError('not found', 404, 'not_found');
    return j.job;
  }

  cancel(id: string): void {
    const j = this.jobs.get(id);
    if (!j || j.job.status !== 'running') return;
    if (j.timer) clearInterval(j.timer);
    j.job.status = 'canceled';
    j.job.resumable = j.job.kind === 'import';
    j.job.finished = new Date().toISOString();
    this.emit({ type: 'io.job', itemId: id, state: 'canceled' });
  }

  downloadUrl(token: string): string {
    return `data:application/octet-stream;base64,${btoa('TuxMail demo download ' + token)}`;
  }

  private manifest(accounts: Account[], mode: BackupManifest['scope']['mode'] = 'full', encrypted = true): BackupManifest {
    return {
      format: 1,
      id: '5f1c9a2e7d3b4a10',
      created: '2026-11-03T01:30:12Z',
      appVersion: '0.1.0',
      schemaVersion: 5,
      scope: { mode, secrets: true },
      encrypted,
      secrets: true,
      files: [{ path: 'db/tuxmail.db', size: 48_234_496, sha256: '9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08' }],
      blobs: [],
      blobsIncluded: 18_211,
      accounts: accounts.map((a) => ({ id: a.id, kind: a.kind, name: a.displayName, address: a.extra?.address, messenger: ['whatsapp', 'signal', 'matrix_bridge'].includes(a.kind), messages: 1200 + a.id * 37 })),
      counts: { messages: 18_211, events: 412, contacts: 318, identities: 4, signatures: 3, workflows: 6 },
      warnings: [],
    };
  }

  createBackup(req: BackupRequest, accounts: Account[]): IOJob {
    if (req.scope.secrets && !req.passphrase) throw new ApiError('a passphrase is required when credentials are included', 400, 'invalid_input');
    return this.start('backup', 'txmb', 40, (j) => {
      j.result = this.manifest(accounts, req.scope.mode, !!req.passphrase);
      if (req.target.kind === 'download') {
        j.download = `iod_mock${this.seq++}`;
        j.downloadName = 'tuxmail-backup-2026-11-03-1015-5f1c9a2e.txmb';
      }
    });
  }

  inspectBackup(source: BackupSource, passphrase: string, accounts: Account[]): BackupInspection {
    if (!source.uploadToken && !source.path && !source.name) throw new ApiError('no archive selected', 400, 'invalid_input');
    if (!passphrase) return { manifest: null, encrypted: true, messenger: false };
    if (passphrase === 'wrong') throw new ApiError('backup: wrong passphrase', 401, 'invalid_password');
    const m = this.manifest(accounts);
    return { manifest: m, encrypted: true, messenger: (m.accounts ?? []).some((a) => a.messenger) };
  }

  startRestore(req: RestoreRequest): IOJob {
    return this.start('restore', 'txmb', 30, (j) => {
      const accounts = req.accounts?.length ?? 3;
      j.summary = { accounts, messages: req.configOnly ? 0 : 9_412, blobs: req.configOnly ? 0 : 9_412, secrets: req.secrets ? accounts : 0 };
      j.result = { mode: req.mode, accounts, messages: j.summary.messages, blobs: j.summary.blobs, secrets: j.summary.secrets, schemaFrom: 4, schemaTo: 5, restartRequired: req.mode === 'replace' };
    });
  }

  schedulesList(): BackupSchedule[] {
    return this.schedules;
  }

  saveSchedule(s: BackupScheduleInput): BackupSchedule {
    const { passphrase, targetPassword, ...rest } = s;
    const out: BackupSchedule = { ...rest, id: s.id || `bk_mock${this.seq++}`, keepDaily: s.keepDaily || 7, keepWeekly: s.keepWeekly || 4 };
    if (passphrase) out.passphraseRef = `backup:${out.id}:passphrase`;
    if (targetPassword) out.target = { ...out.target, passwordRef: `backup:${out.id}:webdav` };
    const next = new Date();
    next.setDate(next.getDate() + 1);
    next.setHours(out.hour, out.minute, 0, 0);
    out.nextRun = next.toISOString();
    const i = this.schedules.findIndex((x) => x.id === out.id);
    if (i >= 0) this.schedules[i] = out;
    else this.schedules.push(out);
    return out;
  }

  deleteSchedule(id: string): void {
    this.schedules = this.schedules.filter((s) => s.id !== id);
  }

  runSchedule(id: string, accounts: Account[]): IOJob {
    const s = this.schedules.find((x) => x.id === id);
    if (!s) throw new ApiError('not found', 404, 'not_found');
    return this.start('backup', 'txmb', 40, (j) => {
      j.result = this.manifest(accounts, s.scope.mode);
      s.lastRun = new Date().toISOString();
      s.lastError = '';
    });
  }

  archives(id: string): BackupArchive[] {
    if (!this.schedules.some((s) => s.id === id)) throw new ApiError('not found', 404, 'not_found');
    return [
      { name: 'tuxmail-backup-2026-11-03-0230-5f1c9a2e.incr-91b3d0aa.txmb', size: 3_481_220, modified: '2026-11-03T01:31:02Z' },
      { name: 'tuxmail-backup-2026-11-02-0230-2c77e0b1.incr-91b3d0aa.txmb', size: 2_904_118, modified: '2026-11-02T01:30:57Z' },
      { name: 'tuxmail-backup-2026-11-01-0230-91b3d0aa.txmb', size: 812_330_442, modified: '2026-11-01T01:42:10Z' },
    ];
  }
}
