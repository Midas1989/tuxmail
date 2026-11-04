// SPDX-License-Identifier: AGPL-3.0-or-later
// Browser mock of the Wails bridge of the setup window, for development and
// screenshots (frontend/scripts/setup-screenshots.cjs). Not embedded into
// the setup program. Query parameters:
//   mode=uninstall   uninstall page
//   existing=upgrade|reinstall   an installed TuxMail (per user)
//   lang=de|en, running=1 (TuxMail is running), fail=1 (install fails),
//   hold=<percent> (progress stops there), elevated=1
(() => {
  const q = new URLSearchParams(location.search);
  const listeners = {};
  const emit = (name, data) => (listeners[name] || []).forEach((f) => f(data));
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const version = '0.2.0';
  const existing = q.get('existing')
    ? [{ scope: 'user', dir: 'C:\\Users\\anna\\AppData\\Local\\Programs\\TuxMail', version: q.get('existing') === 'reinstall' ? version : '0.1.4',
        options: { startMenu: true, desktop: true, defaultMail: false, autostart: false }, legacy: false, kind: q.get('existing') }]
    : [];
  const dirs = {
    user: 'C:\\Users\\anna\\AppData\\Local\\Programs\\TuxMail',
    machine: 'C:\\Program Files\\TuxMail',
    portable: 'E:\\TuxMail',
  };
  let running = q.get('running') === '1';

  async function progress(steps) {
    const hold = Number(q.get('hold') || 0);
    for (const [step, from, to, files] of steps) {
      const n = files ? files.length : 4;
      for (let i = 0; i <= n; i++) {
        const percent = from + ((to - from) * i) / n;
        emit('setup:progress', { step, percent, file: files ? files[Math.min(i, n - 1)] : '' });
        if (hold && percent >= hold) await new Promise(() => {}); // freeze for screenshots
        await sleep(60);
      }
    }
  }
  const files = ['LICENSE.txt', 'NOTICE.txt', 'TuxMail.exe', 'plugins/tuxmail-plugin-matrix.exe', 'plugins/tuxmail-plugin-signal.exe',
    'plugins/tuxmail-plugin-telegram.exe', 'plugins/tuxmail-plugin-whatsapp.exe'];

  window.runtime = {
    EventsOn(name, cb) {
      (listeners[name] = listeners[name] || []).push(cb);
      return () => {};
    },
  };
  window.go = {
    main: {
      Setup: {
        async State() {
          return {
            mode: q.get('mode') === 'uninstall' ? 'uninstall' : 'install',
            version, arch: 'amd64', lang: q.get('lang') || 'de', dark: false, mica: q.get('mica') === '1',
            elevated: q.get('elevated') === '1', windowsBuild: 22631, webview2: '130.0.2849.80',
            sizeBytes: 187 * 1024 * 1024 + 345678, existing, dirs: { ...dirs, ...Object.fromEntries(existing.map((e) => [e.scope, e.dir])) },
            dataDir: 'C:\\Users\\anna\\AppData\\Roaming\\TuxMail',
            uninstall: q.get('mode') === 'uninstall' ? { scope: 'user', dir: dirs.user, version: '0.1.4' } : undefined,
          };
        },
        async DefaultDir(scope) { return dirs[scope]; },
        async CheckDir(scope, dir) {
          const d = String(dir || '').trim();
          if (!d) return { dir: d, error: 'dirEmpty', existing: false };
          if (!/^[a-zA-Z]:\\/.test(d) && !d.startsWith('\\\\')) return { dir: d, error: 'dirRelative', existing: false };
          if (/^[a-zA-Z]:\\?$/.test(d)) return { dir: d, error: 'dirRoot', existing: false };
          if (/^c:\\windows/i.test(d)) return { dir: d, error: 'dirSystem', existing: false };
          if (/^c:\\program files\\?$/i.test(d)) return { dir: d, error: 'dirProtected', existing: false };
          const ex = existing.some((e) => e.dir.toLowerCase() === d.toLowerCase());
          return { dir: d, existing: ex, warning: /\\data\\?$/i.test(d) ? 'dirNotEmpty' : undefined };
        },
        async Browse() { return 'D:\\Programme\\TuxMail'; },
        async Running() { return running ? 1 : 0; },
        async SetLanguage() {},
        async Install(req) {
          running = false;
          const steps = [['prepare', 0, 3], ['files', 3, 84, files]];
          if (req.scope !== 'portable') steps.push(['shortcuts', 84, 90], ['register', 90, 97]);
          await progress(steps);
          if (q.get('fail') === '1') throw 'TuxMail.exe: open C:\\Program Files\\TuxMail\\TuxMail.exe.tuxmail-new: Access is denied.';
        },
        async Uninstall() {
          await progress([['remove', 0, 20], ['register', 20, 40], ['files', 40, 95, files]]);
        },
        async Launch() {},
        async OpenDefaultApps() {},
        async OpenURL(u) { window.open(u, '_blank'); },
        async OpenFolder() {},
        Quit() {},
        Minimise() {},
      },
    },
  };
})();
