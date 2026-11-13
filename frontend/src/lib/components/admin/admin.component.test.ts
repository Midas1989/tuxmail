// @vitest-environment jsdom
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Behaviour of the administration screens, mounted in jsdom against the
// in-memory MockAdminClient: unlock only where an account is throttled, the
// guards against self-lockout and removing the last administrator, and a
// generated password that is shown exactly once.

import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, tick, unmount } from 'svelte';
import { MockAdminClient, type AdminClient, type AdminMe } from '../../api/admin';
import { ApiError } from '../../api/api';
import type { AdminPanelProps } from '../../admin/panel';
import AdminUsers from './AdminUsers.svelte';
import AdminInvites from './AdminInvites.svelte';
import AdminGroups from './AdminGroups.svelte';
import AdminAudit from './AdminAudit.svelte';
import AdminBackup from './AdminBackup.svelte';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Comp = any;
const mounted: { cmp: ReturnType<typeof mount>; el: HTMLElement }[] = [];

const ME: AdminMe = { id: MockAdminClient.ME, username: 'admin', role: 'admin', mfaSetupRequired: false };

async function settle() {
  for (let i = 0; i < 8; i++) {
    await tick();
    await new Promise((r) => setTimeout(r, 0));
  }
  flushSync();
}

function show(cmp: Comp, props: object) {
  const el = document.createElement('div');
  document.body.append(el);
  const instance = mount(cmp, { target: el, props });
  mounted.push({ cmp: instance, el });
  return el;
}

function panel(client: AdminClient, extra: Partial<AdminPanelProps> = {}): AdminPanelProps & { notify: ReturnType<typeof vi.fn> } {
  return { client, me: ME, config: { registration: 'invite', passwordMinLength: 12 }, notify: vi.fn(), ...extra } as never;
}

// jsdom has no Web Animations API: Modal's fade/scale transitions finish at once.
beforeAll(() => {
  Object.defineProperty(Element.prototype, 'animate', {
    configurable: true,
    value() {
      const a: { onfinish: (() => void) | null; cancel(): void; finish(): void; currentTime: number; finished: Promise<void> } = {
        onfinish: null,
        cancel() {},
        finish() {},
        currentTime: 0,
        finished: Promise.resolve(),
      };
      setTimeout(() => a.onfinish?.(), 0);
      return a;
    },
  });
});

afterEach(async () => {
  for (const m of mounted.splice(0)) {
    await unmount(m.cmp, { outro: false });
    m.el.remove();
  }
  document.body.innerHTML = '';
});

const row = (name: string) => document.querySelector<HTMLTableRowElement>(`tr[data-user="${name}"]`)!;
const rowBtn = (name: string, action: string) => row(name).querySelector<HTMLButtonElement>(`button[data-action="${action}"]`);
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const dlgBtn = (action: string) => dialog()!.querySelector<HTMLButtonElement>(`button[data-action="${action}"]`)!;
const byText = (root: ParentNode, text: string) => [...root.querySelectorAll<HTMLButtonElement>('button')].find((b) => b.textContent?.trim() === text)!;

async function openActions(name: string) {
  rowBtn(name, 'manage')!.click();
  await settle();
  expect(dialog()).not.toBeNull();
}

function typeInto(input: HTMLInputElement, value: string) {
  input.value = value;
  input.dispatchEvent(new Event('input', { bubbles: true }));
  flushSync();
}

/** Answers the step-up form (M-2) that creating users, resetting passwords and the like ask for. */
async function proof(password = 'admin-password') {
  const form = document.querySelector<HTMLFormElement>('form.stepup')!;
  expect(form).not.toBeNull();
  typeInto(form.querySelector<HTMLInputElement>('input[type="password"]')!, password);
  form.requestSubmit();
  await settle();
}

describe('Users: unlock', () => {
  it('shows "Unlock" only for locked or attacked accounts', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    show(AdminUsers, panel(c));
    await settle();
    expect(row('ben')).toBeTruthy();
    expect(rowBtn('ben', 'unlock')).toBeTruthy(); // locked network
    expect(rowBtn('carla', 'unlock')).toBeTruthy(); // under pressure
    for (const n of ['admin', 'anna', 'dieter', 'erika']) expect(rowBtn(n, 'unlock'), n).toBeNull();
    expect(row('ben').querySelector('[data-testid="lock-info"]')).not.toBeNull();
    expect(row('carla').querySelector('[data-testid="pressure-info"]')).not.toBeNull();
  });

  it('unlocking calls the server, reloads and removes the button', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    const unlock = vi.spyOn(c, 'unlock');
    const p = panel(c);
    show(AdminUsers, p);
    await settle();
    rowBtn('ben', 'unlock')!.click();
    await settle();
    expect(unlock).toHaveBeenCalledTimes(1);
    expect(rowBtn('ben', 'unlock')).toBeNull();
    expect(rowBtn('carla', 'unlock')).toBeTruthy();
    expect(p.notify).toHaveBeenCalledWith(expect.stringContaining('ben'));
  });

  it('the status filter "Locked" lists exactly the accounts that can be unlocked', async () => {
    show(AdminUsers, panel(new MockAdminClient({ latencyMs: 0 })));
    await settle();
    const sel = document.querySelector<HTMLSelectElement>('[data-testid="user-status-filter"]')!;
    sel.value = 'locked';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    await settle();
    expect([...document.querySelectorAll('tr[data-user]')].map((r) => r.getAttribute('data-user'))).toEqual(['ben', 'carla']);
  });

  it('the unlock action of the actions dialog is offered only for a throttled account', async () => {
    show(AdminUsers, panel(new MockAdminClient({ latencyMs: 0 })));
    await settle();
    await openActions('anna');
    expect(dialog()!.querySelector('button[data-action="unlock"]')).toBeNull();
    byText(dialog()!, 'Close').click();
    await settle();
    await openActions('ben');
    expect(dlgBtn('unlock')).toBeTruthy();
  });
});

describe('Users: search', () => {
  it('filters the table by name, display name and e-mail', async () => {
    show(AdminUsers, panel(new MockAdminClient({ latencyMs: 0 })));
    await settle();
    const q = document.querySelector<HTMLInputElement>('[data-testid="user-search"]')!;
    typeInto(q, 'keller');
    await settle();
    expect([...document.querySelectorAll('tr[data-user]')].map((r) => r.getAttribute('data-user'))).toEqual(['ben']);
    typeInto(q, 'zzz');
    await settle();
    expect(document.querySelectorAll('tr[data-user]')).toHaveLength(0);
  });
});

describe('Users: guards against locking out the administration', () => {
  it('an administrator cannot demote, disable, delete or reset themself', async () => {
    show(AdminUsers, panel(new MockAdminClient({ latencyMs: 0 })));
    await settle();
    await openActions('admin');
    for (const a of ['toggleDisabled', 'resetPassword', 'reset2fa', 'delete']) expect(dlgBtn(a).disabled, a).toBe(true);
    expect(dlgBtn('edit').disabled).toBe(false);
    expect(dlgBtn('sessions').disabled).toBe(false);
    // The reason is written next to the button.
    expect(dialog()!.textContent).toMatch(/own account|sign you out/);
    // The role selector of the edit form is locked, too.
    dlgBtn('edit').click();
    await settle();
    expect(document.querySelector<HTMLSelectElement>('#au-role')!.disabled).toBe(true);
  });

  it('other accounts can be managed while a second administrator exists', async () => {
    show(AdminUsers, panel(new MockAdminClient({ latencyMs: 0 })));
    await settle();
    await openActions('erika'); // second administrator
    for (const a of ['toggleDisabled', 'resetPassword', 'reset2fa', 'delete']) expect(dlgBtn(a).disabled, a).toBe(false);
  });

  it('the only enabled administrator cannot be disabled, deleted or demoted', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    // Make "erika" the only other administrator and then disable her: "admin" (me) is last, but guard for
    // somebody else's view: a second session of the same server where the viewer is erika.
    await c.updateUser(6, { role: 'user' });
    const p = panel(c, { me: { ...ME, id: 6, username: 'erika' } });
    show(AdminUsers, p);
    await settle();
    await openActions('admin');
    expect(dlgBtn('toggleDisabled').disabled).toBe(true);
    expect(dlgBtn('delete').disabled).toBe(true);
    expect(dialog()!.textContent).toMatch(/only enabled administrator/);
    // Not self, so password reset stays possible.
    expect(dlgBtn('resetPassword').disabled).toBe(false);
  });

  it('shows a server refusal (last_admin) in the UI language when the list was stale', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    // The viewer is somebody else, so only the last-administrator rule applies.
    show(AdminUsers, panel(c, { me: { ...ME, id: 999, username: 'other' } }));
    await settle();
    await openActions('admin'); // two administrators in the list: allowed
    expect(dlgBtn('delete').disabled).toBe(false);
    byText(dialog()!, 'Close').click();
    await settle();
    await c.updateUser(6, { role: 'user' }); // meanwhile somebody else demoted the other one
    await openActions('admin');
    dlgBtn('delete').click();
    await settle();
    typeInto(document.querySelector<HTMLInputElement>('[data-testid="delete-confirm"]')!, 'admin');
    document.querySelector<HTMLButtonElement>('[data-testid="delete-submit"]')!.click();
    await settle();
    await proof(); // deleting an administrator asks for a fresh proof
    expect(dialog()!.querySelector('[role="alert"]')?.textContent).toBe('This is the only enabled administrator. Make another account administrator first.');
    expect(row('admin')).toBeTruthy();
  });

  it('delete needs the typed user name and passes "keep data"', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    const del = vi.spyOn(c, 'deleteUser');
    show(AdminUsers, panel(c));
    await settle();
    await openActions('anna');
    dlgBtn('delete').click();
    await settle();
    const submit = document.querySelector<HTMLButtonElement>('[data-testid="delete-submit"]')!;
    expect(submit.disabled).toBe(true);
    const input = document.querySelector<HTMLInputElement>('[data-testid="delete-confirm"]')!;
    typeInto(input, 'ann');
    expect(submit.disabled).toBe(true);
    typeInto(input, 'anna');
    expect(submit.disabled).toBe(false);
    const keep = dialog()!.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    keep.click();
    flushSync();
    submit.click();
    await settle();
    expect(del).toHaveBeenCalledWith(2, true, undefined); // a plain user needs no step-up
    expect(row('anna')).toBeNull();
  });
});

describe('Users: one-time password', () => {
  it('shows a generated password once after creating a user', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    show(AdminUsers, panel(c));
    await settle();
    document.querySelector<HTMLButtonElement>('[data-testid="user-create"]')!.click();
    await settle();
    typeInto(document.querySelector<HTMLInputElement>('#au-username')!, 'neu');
    document.querySelector<HTMLButtonElement>('[data-testid="user-form-submit"]')!.click();
    await settle();
    await proof();

    const field = document.querySelector<HTMLInputElement>('[data-testid="secret-value"]');
    expect(field).not.toBeNull();
    const password = field!.value;
    expect(password.length).toBeGreaterThanOrEqual(20);
    expect(row('neu')).toBeTruthy();

    // Esc / click outside must not throw the secret away before it was copied.
    dialog()!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await settle();
    expect(document.querySelector('[data-testid="secret-value"]')).not.toBeNull();

    // Copy, then "Done" closes it, and the password is nowhere in the page any more.
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn(async () => {}) }, configurable: true });
    document.querySelector<HTMLButtonElement>('[data-testid="secret-copy"]')!.click();
    await settle();
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(password);
    document.querySelector<HTMLButtonElement>('[data-testid="secret-done"]')!.click();
    await settle();
    expect(document.querySelector('[data-testid="secret-value"]')).toBeNull();
    expect(document.body.textContent).not.toContain(password);
    expect(document.body.innerHTML).not.toContain(password);

    // Opening the actions of the user never brings it back.
    await openActions('neu');
    expect(document.body.innerHTML).not.toContain(password);
  });

  it('does not show a password when the administrator chose one', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    show(AdminUsers, panel(c));
    await settle();
    document.querySelector<HTMLButtonElement>('[data-testid="user-create"]')!.click();
    await settle();
    typeInto(document.querySelector<HTMLInputElement>('#au-username')!, 'eigen');
    const custom = document.querySelector<HTMLInputElement>('input[name="au-pw"][value="custom"]')!;
    custom.click();
    flushSync();
    typeInto(document.querySelector<HTMLInputElement>('#au-password')!, 'a-very-long-passphrase');
    document.querySelector<HTMLButtonElement>('[data-testid="user-form-submit"]')!.click();
    await settle();
    await proof();
    expect(document.querySelector('[data-testid="secret-value"]')).toBeNull();
    expect(row('eigen')).toBeTruthy();
  });

  it('reports a policy error of the server in the dialog (localized) and keeps it open', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    show(AdminUsers, panel(c));
    await settle();
    document.querySelector<HTMLButtonElement>('[data-testid="user-create"]')!.click();
    await settle();
    typeInto(document.querySelector<HTMLInputElement>('#au-username')!, 'weak');
    document.querySelector<HTMLInputElement>('input[name="au-pw"][value="custom"]')!.click();
    flushSync();
    typeInto(document.querySelector<HTMLInputElement>('#au-password')!, 'short');
    document.querySelector<HTMLButtonElement>('[data-testid="user-form-submit"]')!.click();
    await settle();
    await proof();
    const alert = dialog()!.querySelector('[role="alert"]');
    expect(alert?.textContent).toMatch(/at least 12 characters/);
    expect(row('weak')).toBeNull();
  });

  it('a reset password is shown once as well, after confirming', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    const reset = vi.spyOn(c, 'resetPassword');
    show(AdminUsers, panel(c));
    await settle();
    await openActions('anna');
    dlgBtn('resetPassword').click();
    await settle();
    expect(reset).not.toHaveBeenCalled(); // asks first
    byText(dialog()!, 'Reset password').click();
    await settle();
    expect(reset).not.toHaveBeenCalled(); // and asks for the administrator's proof
    await proof('my-admin-password');
    expect(reset).toHaveBeenCalledWith(2, expect.objectContaining({ password: 'my-admin-password' }));
    const pw = document.querySelector<HTMLInputElement>('[data-testid="secret-value"]')!.value;
    expect(pw).toHaveLength(20);
    document.querySelector<HTMLButtonElement>('[data-testid="secret-done"]')!.click();
    await settle();
    expect(document.body.innerHTML).not.toContain(pw);
  });
});

describe('Invites', () => {
  it('creates an invite and shows the link once', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    show(AdminInvites, panel(c));
    await settle();
    expect(document.querySelectorAll('[data-testid="invite-table"] tbody tr')).toHaveLength(1);
    document.querySelector<HTMLButtonElement>('[data-testid="invite-create"]')!.click();
    await settle();
    const link = document.querySelector<HTMLInputElement>('[data-testid="secret-value"]')!.value;
    expect(link).toMatch(/\/register\?invite=/);
    expect(document.querySelectorAll('[data-testid="invite-table"] tbody tr')).toHaveLength(2);
    document.querySelector<HTMLButtonElement>('[data-testid="secret-done"]')!.click();
    await settle();
    expect(document.body.innerHTML).not.toContain(link);
  });

  it('explains and blocks creation when the server has invites switched off', async () => {
    show(AdminInvites, panel(new MockAdminClient({ latencyMs: 0 }), { config: { registration: 'closed', passwordMinLength: 12 } }));
    await settle();
    expect(document.querySelector('[data-testid="invites-off"]')).not.toBeNull();
    expect(document.querySelector<HTMLButtonElement>('[data-testid="invite-create"]')!.disabled).toBe(true);
  });
});

describe('Groups', () => {
  it('lists groups, creates one and manages members', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    show(AdminGroups, panel(c));
    await settle();
    expect(document.querySelector('[data-group="Sekretariat"]')).toBeTruthy();
    typeInto(document.querySelector<HTMLInputElement>('#grp-name')!, 'Einkauf');
    document.querySelector<HTMLButtonElement>('[data-testid="group-create"]')!.click();
    await settle();
    const g = document.querySelector<HTMLElement>('[data-group="Einkauf"]')!;
    expect(g).toBeTruthy();
    const sel = g.querySelector<HTMLSelectElement>('select')!;
    sel.value = '2';
    sel.dispatchEvent(new Event('change', { bubbles: true }));
    flushSync();
    byText(g, 'Add').click();
    await settle();
    expect(document.querySelector('[data-group="Einkauf"] .chip')?.textContent).toContain('anna');
    document.querySelector<HTMLButtonElement>('[data-group="Einkauf"] .chip .x')!.click();
    await settle();
    expect(document.querySelector('[data-group="Einkauf"] .chip')).toBeNull();
  });
});

describe('Audit log', () => {
  it('shows the entries, filters, and pages older ones', async () => {
    const c = new MockAdminClient({ latencyMs: 0 });
    for (let i = 0; i < 60; i++) await c.createGroup(`g${i}`, '');
    const spy = vi.spyOn(c, 'auditLog');
    show(AdminAudit, panel(c));
    await settle();
    expect(document.querySelectorAll('[data-testid="audit-table"] tbody tr')).toHaveLength(50);
    byText(document.body, 'Load older entries').click();
    await settle();
    expect(spy).toHaveBeenLastCalledWith(expect.objectContaining({ before: expect.any(Number), limit: 50 }));
    expect(document.querySelectorAll('[data-testid="audit-table"] tbody tr').length).toBeGreaterThan(50);

    typeInto(document.querySelector<HTMLInputElement>('#aud-action')!, 'login');
    document.querySelector<HTMLButtonElement>('[data-testid="audit-apply"]')!.click();
    await settle();
    expect(spy).toHaveBeenLastCalledWith(expect.objectContaining({ action: 'login' }));
    const rows = [...document.querySelectorAll('[data-testid="audit-table"] tbody tr')];
    expect(rows.length).toBe(1);
    expect(rows[0].textContent).toContain('login');
  });
});

describe('Tables are accessible', () => {
  it('have captions, column and row headers and keyboard-reachable scroll regions', async () => {
    show(AdminUsers, panel(new MockAdminClient({ latencyMs: 0 })));
    await settle();
    const table = document.querySelector('[data-testid="user-table"]')!;
    expect(table.querySelector('caption')).not.toBeNull();
    expect(table.querySelectorAll('thead th[scope="col"]').length).toBe(7);
    expect(table.querySelectorAll('tbody th[scope="row"]').length).toBeGreaterThan(0);
    expect(table.closest('[role="region"]')?.getAttribute('tabindex')).toBe('0');
    // Every row action has an accessible name that says which user it is for.
    for (const b of table.querySelectorAll('button')) expect((b.getAttribute('aria-label') ?? b.textContent ?? '').trim().length).toBeGreaterThan(0);
    expect(rowBtn('ben', 'unlock')!.getAttribute('aria-label')).toContain('ben');
  });
});

describe('AdminBackup', () => {
  const REAUTH_PW = 'correct horse battery staple';

  async function confirmWithPassword(el: HTMLElement) {
    const pw = document.querySelector<HTMLInputElement>('form.stepup input[type=password]')!;
    pw.value = REAUTH_PW;
    pw.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
    document.querySelector<HTMLButtonElement>('form.stepup button[type=submit]')!.click();
    await settle();
    return el;
  }

  it('warns without a key, shows the private key once and only closes after the vault confirmation', async () => {
    const client = new MockAdminClient({ latencyMs: 0 });
    const el = show(AdminBackup, panel(client));
    await settle();
    expect(el.querySelector('[data-testid=backup-nokey]')).toBeTruthy();

    el.querySelector<HTMLButtonElement>('[data-testid=backup-generate]')!.click();
    await settle();
    expect(document.querySelector('[data-testid=backup-replace-warning]')).toBeNull();
    await confirmWithPassword(el);

    const value = document.querySelector<HTMLInputElement>('[data-testid=secret-value]')!;
    expect(value.value).toMatch(/^AGE-SECRET-KEY-/);
    const done = document.querySelector<HTMLButtonElement>('[data-testid=secret-done]')!;
    expect(done.disabled).toBe(true);
    const ack = document.querySelector<HTMLInputElement>('[data-testid=secret-ack]')!;
    ack.click();
    flushSync();
    expect(done.disabled).toBe(false);
    // Until the server has it, the key does not count and the dialog stays.
    expect((await client.backupStatus()).keys).toHaveLength(0);
    const confirm = vi.spyOn(client, 'confirmBackupKey').mockRejectedValueOnce(new ApiError('down', 0, 'network'));
    done.click();
    await settle();
    expect(document.querySelector('[data-testid=secret-value]')).toBeTruthy();
    done.click();
    await settle();
    expect(confirm).toHaveBeenCalledTimes(2);

    expect(document.querySelector('[data-testid=secret-value]')).toBeNull();
    expect(document.body.innerHTML).not.toContain('AGE-SECRET-KEY-');
    expect(el.querySelector('[data-testid=backup-nokey]')).toBeNull();
    expect(el.querySelector('[data-testid=backup-key]')).toBeTruthy();
  });

  it('keeps the old key when the dialog is given up and says a key is waiting', async () => {
    const client = new MockAdminClient({ latencyMs: 0 });
    const created = await client.generateBackupKey({ password: REAUTH_PW }, false);
    await client.confirmBackupKey(created.fingerprint, false);
    const el = show(AdminBackup, panel(client));
    await settle();
    el.querySelector<HTMLButtonElement>('[data-testid=backup-generate]')!.click();
    await settle();
    await confirmWithPassword(el);
    document.querySelector<HTMLButtonElement>('[data-testid=secret-cancel]')!.click();
    await settle();
    expect(document.querySelector('[data-testid=secret-value]')).toBeNull();
    const st = await client.backupStatus();
    expect(st.keys[0].fingerprint).toBe(created.fingerprint);
    expect(el.querySelector('[data-testid=backup-pending]')).toBeTruthy();
  });

  it('warns before replacing and refuses a private key in the import field', async () => {
    const client = new MockAdminClient({ latencyMs: 0 });
    const key = await client.generateBackupKey({ password: REAUTH_PW }, false);
    await client.confirmBackupKey(key.fingerprint, false);
    const el = show(AdminBackup, panel(client));
    await settle();

    el.querySelector<HTMLButtonElement>('[data-testid=backup-own-toggle]')!.click();
    flushSync();
    const own = el.querySelector<HTMLInputElement>('[data-testid=backup-own]')!;
    own.value = 'AGE-SECRET-KEY-1ABC';
    own.dispatchEvent(new Event('input', { bubbles: true }));
    flushSync();
    el.querySelector<HTMLButtonElement>('[data-testid=backup-own-submit]')!.click();
    await settle();
    expect(document.querySelector('[data-testid=backup-replace-warning]')).toBeTruthy();
    await confirmWithPassword(el);
    expect(document.querySelector('form.stepup [role=alert]')?.textContent).toContain('age1');
  });
});
