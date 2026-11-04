// SPDX-License-Identifier: AGPL-3.0-or-later
// TuxMail Setup window. Talks to Go through Wails bindings
// (window.go.main.Setup.*) and the "setup:progress" event; in a browser
// cmd/tuxmail-setup/ui-dev/mock.js provides the same interface.
(() => {
  'use strict';

  // ------------------------------------------------------------ icons
  const P = {
    user: '<circle cx="12" cy="8" r="4"/><path d="M4 21c1.2-4 4.4-6 8-6s6.8 2 8 6"/>',
    users: '<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20c.9-3.4 3.4-5.2 6.5-5.2s5.6 1.8 6.5 5.2"/><path d="M15.5 4.8a3.5 3.5 0 0 1 0 6.4M18 14.6c1.8.7 3 2.5 3.5 5.4"/>',
    usb: '<rect x="7" y="9" width="10" height="13" rx="2"/><path d="M9 9V3h6v6M10.5 5.5h0M13.5 5.5h0"/>',
    folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
    check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
    shield: '<path d="M12 3l7.5 3v5.5c0 4.6-3.2 8.3-7.5 9.5-4.3-1.2-7.5-4.9-7.5-9.5V6z"/>',
    mail: '<rect x="3" y="5" width="18" height="14" rx="2.5"/><path d="M3.5 7l8.5 6.5L20.5 7"/>',
    calendar: '<rect x="3.5" y="5" width="17" height="15.5" rx="2.5"/><path d="M3.5 10h17M8 3v4M16 3v4"/>',
    contacts: '<rect x="4" y="3" width="16" height="18" rx="2.5"/><circle cx="12" cy="10" r="3"/><path d="M7.5 17.5c.8-2 2.5-3 4.5-3s3.7 1 4.5 3"/>',
    chat: '<path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4h0A2.5 2.5 0 0 1 4 13.5z"/>',
    start: '<rect x="3.5" y="3.5" width="7" height="7" rx="1.2"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.2"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.2"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.2"/>',
    desktop: '<rect x="3" y="4" width="18" height="12.5" rx="2"/><path d="M8.5 20.5h7M12 16.5v4"/>',
    at: '<circle cx="12" cy="12" r="3.8"/><path d="M15.8 12v1.6a2.6 2.6 0 0 0 5.2 0V12a9 9 0 1 0-3.6 7.2"/>',
    power: '<path d="M12 3v8"/><path d="M6.6 6.6a8 8 0 1 0 10.8 0"/>',
    info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v5.5M12 7.6v.1"/>',
    alert: '<path d="M12 3.5l9.5 16.5h-19z"/><path d="M12 10v4.5M12 17.4v.1"/>',
    x: '<path d="M6 6l12 12M18 6L6 18"/>',
    arrowLeft: '<path d="M19 12H5M11 6l-6 6 6 6"/>',
    arrowRight: '<path d="M5 12h14M13 6l6 6-6 6"/>',
    rocket: '<path d="M12 15l-3-3c1.5-4.5 4.7-8 10-8.5-.5 5.3-4 8.5-8.5 10z"/><path d="M9 12l-4 .5L7 9.5M12 15l-.5 4L14.5 17M6.5 17.5c-1 1-1.5 3-1.5 3s2-.5 3-1.5"/>',
    trash: '<path d="M4 7h16M9.5 7V4.5h5V7M6.5 7l1 13h9l1-13"/>',
    external: '<path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/>',
  };
  const icon = (n) => `<svg viewBox="0 0 24 24" aria-hidden="true" stroke-linecap="round" stroke-linejoin="round">${P[n] || ''}</svg>`;

  // ------------------------------------------------------------ helpers
  const $ = (s) => document.querySelector(s);
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
  let lang = 'de';
  const t = (key, vars) => {
    const d = window.SETUP_I18N;
    let s = (d[lang] && d[lang][key]) ?? d.en[key] ?? key;
    if (vars) s = s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? vars[k] : m));
    return s;
  };
  const fmtSize = (bytes) => {
    const mb = bytes / (1024 * 1024);
    return new Intl.NumberFormat(lang === 'de' ? 'de-DE' : 'en-US', { maximumFractionDigits: mb < 10 ? 1 : 0 }).format(mb) + ' MB';
  };

  // ------------------------------------------------------------ bridge
  async function bridge() {
    for (let i = 0; i < 100; i++) {
      if (window.go && window.go.main && window.go.main.Setup) return window.go.main.Setup;
      await new Promise((r) => setTimeout(r, 30));
    }
    throw new Error('Wails bridge not available');
  }
  let api;

  // ------------------------------------------------------------ state
  const S = {
    info: null,
    page: 'welcome',
    scope: 'user',
    dirs: {},
    dir: '',
    check: null,
    options: { startMenu: true, desktop: false, defaultMail: false, autostart: false },
    progress: { step: 'prepare', percent: 0, file: '' },
    error: '',
    keepData: true,
    busy: false,
  };
  const installPages = ['welcome', 'type', 'dir', 'options', 'progress', 'done'];
  const uninstallPages = ['uninstall', 'progress', 'done'];
  const isUninstall = () => S.info && S.info.mode === 'uninstall';
  const existingFor = (scope) => (S.info.existing || []).find((e) => e.scope === scope);
  const scopeName = (scope) => t({ user: 'scopeUser', machine: 'scopeMachine', portable: 'scopePortable' }[scope] || 'scopeUser');
  const pages = () => {
    if (isUninstall()) return uninstallPages;
    return S.scope === 'portable' ? installPages.filter((p) => p !== 'options') : installPages;
  };

  // ------------------------------------------------------------ sidebar
  function renderSteps() {
    const labels = isUninstall()
      ? { uninstall: 'stepUninstall', progress: 'stepRemoving', done: 'stepDone' }
      : { welcome: 'stepWelcome', type: 'stepType', dir: 'stepDir', options: 'stepOptions', progress: 'stepProgress', done: 'stepDone' };
    const list = pages();
    const cur = list.indexOf(S.page === 'error' ? 'progress' : S.page);
    $('#steps').innerHTML = list
      .map((p, i) => {
        const cls = i < cur || (S.page === 'done' && i === cur) ? 'done' : i === cur ? 'active' : '';
        const dot = cls === 'done' ? icon('check') : String(i + 1);
        return `<li class="${cls}"><span class="dot">${dot}</span>${esc(t(labels[p]))}</li>`;
      })
      .join('');
  }

  // ------------------------------------------------------------ pages
  function existingNotice() {
    const e = (S.info.existing || [])[0];
    if (!e) return '';
    const key = { upgrade: 'existingUpgrade', reinstall: 'existingReinstall', downgrade: 'existingDowngrade' }[e.kind] || 'existingUpgrade';
    return `<div class="notice">${icon('info')}<div>${esc(t(key, { old: e.version || '?', version: S.info.version, scope: scopeName(e.scope) }))}</div></div>`;
  }

  const view = {
    welcome() {
      const arch = t('arch_' + S.info.arch);
      return `
        <div class="hero">
          <img src="logo.png" alt="TuxMail">
          <div>
            <h1>${esc(t('welcomeTitle'))}</h1>
            <p class="lead" style="margin:0 0 10px">${esc(t('welcomeLead'))}</p>
            <div class="meta"><b>${esc(t('version', { version: S.info.version }))}</b> · ${esc(arch)}${S.info.sizeBytes ? ' · ' + esc(fmtSize(S.info.sizeBytes)) : ''}</div>
          </div>
        </div>
        <div class="chips">
          <span class="chip">${icon('mail')}${esc(t('featMail'))}</span>
          <span class="chip">${icon('calendar')}${esc(t('featCalendar'))}</span>
          <span class="chip">${icon('contacts')}${esc(t('featContacts'))}</span>
          <span class="chip">${icon('chat')}${esc(t('featMessenger'))}</span>
        </div>
        <div class="field-label">${esc(t('language'))}</div>
        <div class="segmented" role="group" aria-label="${esc(t('language'))}">
          <button data-action="lang" data-lang="de" aria-pressed="${lang === 'de'}"><span class="flag flag-de"></span>Deutsch</button>
          <button data-action="lang" data-lang="en" aria-pressed="${lang === 'en'}"><span class="flag flag-en"></span>English</button>
        </div>
        ${existingNotice()}
        ${S.info.payloadError ? `<div class="notice error">${icon('alert')}<div>${esc(t('noPayload'))}</div></div>` : ''}
        <p class="license">${esc(t('license'))} <a data-action="license">${esc(t('licenseLink'))}</a></p>`;
    },

    type() {
      const card = (scope, ico, badges) => {
        const ex = existingFor(scope);
        const inst = ex ? `<span class="badge ok">${esc(t('badgeInstalled', { version: ex.version || '?' }))}</span>` : '';
        const key = { user: 'scopeUser', machine: 'scopeMachine', portable: 'scopePortable' }[scope];
        return `<button class="card" role="radio" aria-checked="${S.scope === scope}" data-action="scope" data-scope="${scope}">
            <span class="ico">${icon(ico)}</span>
            <span class="txt"><span class="ttl">${esc(t(key))}${badges}${inst}</span><span class="dsc">${esc(t(key + 'Desc'))}</span></span>
            <span class="radio"></span>
          </button>`;
      };
      return `
        <h1>${esc(t('typeTitle'))}</h1>
        <p class="lead">${esc(t('typeLead'))}</p>
        <div class="cards" role="radiogroup">
          ${card('user', 'user', `<span class="badge">${esc(t('badgeRecommended'))}</span>`)}
          ${card('machine', 'users', S.info.elevated ? '' : `<span class="badge navy">${icon('shield')}${esc(t('badgeAdmin'))}</span>`)}
          ${card('portable', 'usb', '')}
        </div>`;
    },

    dir() {
      const c = S.check || {};
      let msg = '';
      if (c.error) msg = `<div class="notice error">${icon('alert')}<div>${esc(t(c.error))}</div></div>`;
      else if (c.existing) msg = `<div class="notice info">${icon('info')}<div>${esc(t('dirExisting'))}</div></div>`;
      else if (c.warning) msg = `<div class="notice">${icon('alert')}<div>${esc(t(c.warning))}</div></div>`;
      const lead = { user: 'dirLeadUser', machine: 'dirLeadMachine', portable: 'dirLeadPortable' }[S.scope];
      return `
        <h1>${esc(t('dirTitle'))}</h1>
        <p class="lead">${esc(t(lead))}</p>
        <div class="field-label">${esc(t('dirLabel'))}</div>
        <div class="dir-row">
          <label class="input${c.error ? ' invalid' : ''}">${icon('folder')}<input id="dir" spellcheck="false" autocomplete="off" value="${esc(S.dir)}"></label>
          <button class="btn" data-action="browse">${esc(t('browse'))}</button>
        </div>
        <div class="dir-meta small">${S.info.sizeBytes ? esc(t('space', { size: fmtSize(S.info.sizeBytes) })) : ''}</div>
        <div id="dir-msg">${msg}</div>`;
    },

    options() {
      const row = (key, ico, title, desc) => `
        <button class="opt" role="switch" aria-checked="${!!S.options[key]}" data-action="opt" data-opt="${key}">
          <span class="ico">${icon(ico)}</span>
          <span class="txt"><span class="ttl">${esc(t(title))}</span><br><span class="dsc">${esc(t(desc))}</span></span>
          <span class="switch"></span>
        </button>`;
      const admin = S.scope === 'machine' && !S.info.elevated;
      return `
        <h1>${esc(t('optionsTitle'))}</h1>
        <p class="lead">${esc(t('optionsLead'))}</p>
        <div class="opts">
          ${row('startMenu', 'start', 'optStartMenu', 'optStartMenuDesc')}
          ${row('desktop', 'desktop', 'optDesktop', 'optDesktopDesc')}
          ${row('defaultMail', 'at', 'optDefaultMail', 'optDefaultMailDesc')}
          ${row('autostart', 'power', 'optAutostart', 'optAutostartDesc')}
        </div>
        <div class="summary">${icon('folder')}<span>${esc(scopeName(S.scope))}</span>·<code title="${esc(S.dir)}">${esc(S.dir)}</code></div>
        ${admin ? `<div class="notice info" style="margin-top:10px;padding:9px 14px">${icon('shield')}<div>${esc(t('adminHint'))}</div></div>` : ''}`;
    },

    progress() {
      let title = 'progressTitle';
      if (isUninstall()) title = 'progressTitleUninstall';
      else if (S.scope === 'portable') title = 'progressTitlePortable';
      else if (existingFor(S.scope)) title = 'progressTitleUpdate';
      return `
        <div class="center">
          <div class="orbit"><img src="penguin.png" alt=""><span class="envelope">${icon('mail')}</span></div>
          <h1>${esc(t(title))}</h1>
          <p class="lead" style="margin:0">${esc(t('progressHint'))}</p>
          <div class="bar"><div id="bar"></div></div>
          <div class="progress-step"><span id="pstep"></span><span class="pct" id="ppct"></span></div>
          <div class="progress-file" id="pfile"></div>
        </div>`;
    },

    done() {
      if (isUninstall()) {
        const kept = S.keepData && S.info.dataDir ? `<p class="small">${esc(t('uninstallDoneKept', { dir: S.info.dataDir }))}</p>` : '';
        return `
          <div class="center">
            <div class="big-ico ok-ring">${svgCheck()}</div>
            <h1>${esc(t('uninstallDoneTitle'))}</h1>
            <p class="lead">${esc(t('uninstallDoneLead'))}</p>
            ${kept}
          </div>`;
      }
      const portable = S.scope === 'portable';
      const title = portable ? 'doneTitlePortable' : existingFor(S.scope) ? 'doneTitleUpdate' : 'doneTitle';
      const mail = S.options.defaultMail && !portable
        ? `<div class="notice info" style="text-align:left;max-width:520px">${icon('at')}<div>${esc(t('doneMail'))}
             <div style="margin-top:8px"><button class="btn" data-action="defaultapps">${icon('external')}${esc(t('openDefaultApps'))}</button></div></div></div>`
        : '';
      return `
        <div class="confetti" id="confetti"></div>
        <div class="center">
          <div class="big-ico ok-ring">${svgCheck()}</div>
          <h1>${esc(t(title))}</h1>
          <p class="lead">${esc(t(portable ? 'doneLeadPortable' : 'doneLead'))}</p>
          ${portable ? `<p class="small"><code>${esc(S.dir)}</code></p>` : ''}
          ${mail}
        </div>`;
    },

    error() {
      return `
        <div class="center">
          <div class="big-ico err">${icon('x')}</div>
          <h1>${esc(t('errorTitle'))}</h1>
          <p class="lead">${esc(t(isUninstall() ? 'errorLeadUninstall' : 'errorLead'))}</p>
          <div class="error-box">${esc(errorText(S.error))}</div>
        </div>`;
    },

    uninstall() {
      const u = S.info.uninstall || {};
      return `
        <h1>${esc(t('uninstallTitle'))}</h1>
        <p class="lead">${esc(t('uninstallLead'))}</p>
        <dl class="facts">
          <dt>${esc(t('uninstallVersion'))}</dt><dd>${esc(u.version || '–')}</dd>
          <dt>${esc(t('uninstallType'))}</dt><dd>${esc(scopeName(u.scope))}</dd>
          <dt>${esc(t('uninstallLocation'))}</dt><dd><code title="${esc(u.dir)}">${esc(u.dir)}</code></dd>
        </dl>
        <div class="opts">
          <button class="opt" role="switch" aria-checked="${S.keepData}" data-action="keepdata">
            <span class="ico">${icon('folder')}</span>
            <span class="txt"><span class="ttl">${esc(t('keepData'))}</span><br><span class="dsc">${esc(t('keepDataDesc', { dir: S.info.dataDir || '%APPDATA%\\TuxMail' }))}</span></span>
            <span class="switch"></span>
          </button>
        </div>
        ${S.keepData ? '' : `<div class="notice error">${icon('alert')}<div>${esc(t('removeDataWarn'))}</div></div>`}`;
    },
  };

  const svgCheck = () => '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5"/></svg>';
  const errorText = (e) => {
    const s = String(e && e.message ? e.message : e || '');
    if (s === 'running') return t('err_running');
    if (s === 'elevationCancelled') return t('err_elevationCancelled');
    if (window.SETUP_I18N.en[s]) return t(s); // DirIssue keys
    return s;
  };

  // ------------------------------------------------------------ footer
  function footer() {
    const btn = (action, label, cls = '', ico = '', disabled = false) =>
      `<button class="btn ${cls}" data-action="${action}" ${disabled ? 'disabled' : ''}>${ico}${esc(label)}</button>`;
    const back = btn('back', t('back'), 'btn-ghost', icon('arrowLeft'));
    const cancel = btn('quit', t('cancel'), 'btn-ghost');
    switch (S.page) {
      case 'welcome':
        return `${cancel}<span class="spacer"></span>${btn('next', t('next'), 'btn-primary', '', !!S.info.payloadError)}`;
      case 'type':
        return `${back}<span class="spacer"></span>${btn('next', t('next'), 'btn-primary')}`;
      case 'dir': {
        const invalid = !S.check || !!S.check.error;
        if (S.scope === 'portable') return `${back}<span class="spacer"></span>${btn('install', t('extract'), 'btn-primary', '', invalid)}`;
        return `${back}<span class="spacer"></span>${btn('next', t('next'), 'btn-primary', '', invalid)}`;
      }
      case 'options': {
        const label = existingFor(S.scope) ? t('update') : t('install');
        const shield = S.scope === 'machine' && !S.info.elevated ? icon('shield') : '';
        return `${back}<span class="spacer"></span>${btn('install', label, 'btn-primary', shield)}`;
      }
      case 'progress':
        return '';
      case 'done':
        if (isUninstall()) return `<span class="spacer"></span>${btn('quit', t('finish'), 'btn-primary')}`;
        if (S.scope === 'portable')
          return `<span class="spacer"></span>${btn('openfolder', t('openFolder'), '', icon('folder'))}${btn('launch', t('launch'), 'btn-primary', icon('rocket'))}`;
        return `<span class="spacer"></span>${btn('quit', t('finish'), '')}${btn('launch', t('launch'), 'btn-primary', icon('rocket'))}`;
      case 'error':
        return `${btn('quit', t('close'), 'btn-ghost')}<span class="spacer"></span>${btn('retry', t('retry'), 'btn-primary')}`;
      case 'uninstall':
        return `${cancel}<span class="spacer"></span>${btn('uninstall', t('uninstall'), 'btn-danger', S.info.uninstall && S.info.uninstall.scope === 'machine' && !S.info.elevated ? icon('shield') : icon('trash'))}`;
    }
    return '';
  }

  function render() {
    document.documentElement.lang = lang;
    $('#title').textContent = isUninstall() ? t('uninstallTitle') : t('appTitle');
    $('#tb-min').setAttribute('aria-label', t('minimize'));
    $('#tb-close').setAttribute('aria-label', t('close'));
    $('#tb-close').disabled = S.busy;
    $('#brand-version').textContent = S.info ? t('version', { version: S.info.version }) : '';
    renderSteps();
    $('#content').innerHTML = view[S.page]();
    $('#footer').innerHTML = footer();
    if (S.page === 'progress') paintProgress();
    if (S.page === 'done' && !isUninstall()) confetti();
    const dir = $('#dir');
    if (dir) {
      dir.addEventListener('input', () => {
        S.dir = dir.value;
        scheduleCheck();
      });
    }
  }

  function paintProgress() {
    const p = S.progress;
    const bar = $('#bar');
    if (!bar) return;
    bar.style.width = Math.max(2, Math.min(100, p.percent)) + '%';
    $('#pstep').textContent = t('step_' + p.step);
    $('#ppct').textContent = Math.floor(p.percent) + ' %';
    $('#pfile').textContent = p.file || '';
  }

  function confetti() {
    const box = $('#confetti');
    if (!box || matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    const colors = ['#f59e0b', '#fbbf24', '#1c2b45', '#60a5fa', '#16a34a'];
    let html = '';
    for (let i = 0; i < 28; i++) {
      html += `<i style="left:${(i * 37) % 100}%;background:${colors[i % colors.length]};animation-delay:${(i % 7) * 0.08}s"></i>`;
    }
    box.innerHTML = html;
  }

  // ------------------------------------------------------------ actions
  let checkTimer;
  function scheduleCheck() {
    clearTimeout(checkTimer);
    checkTimer = setTimeout(runCheck, 180);
  }
  async function runCheck() {
    S.check = await api.CheckDir(S.scope, S.dir);
    const box = $('#dir-msg');
    if (S.page !== 'dir' || !box) return;
    // Update only the message and buttons so typing keeps the caret.
    const tmp = document.createElement('div');
    tmp.innerHTML = view.dir();
    box.innerHTML = tmp.querySelector('#dir-msg').innerHTML;
    $('.input').classList.toggle('invalid', !!S.check.error);
    $('#footer').innerHTML = footer();
  }

  async function go(page) {
    if (page === 'dir') {
      S.check = await api.CheckDir(S.scope, S.dir);
    }
    S.page = page;
    render();
  }

  async function chooseScope(scope) {
    S.scope = scope;
    S.dir = S.dirs[scope] || (await api.DefaultDir(scope));
    const ex = existingFor(scope);
    if (ex && ex.options) S.options = { ...ex.options };
    render();
  }

  async function confirmRunning(dir) {
    const n = await api.Running(dir);
    if (!n) return true;
    return new Promise((resolve) => {
      const m = $('#modal');
      m.innerHTML = `<div class="dialog" role="alertdialog" aria-modal="true">
          <h2>${icon('alert')}${esc(t('runningTitle'))}</h2>
          <p>${esc(t('runningText'))}</p>
          <div class="actions">
            <button class="btn btn-ghost" data-m="no">${esc(t('cancel'))}</button>
            <button class="btn btn-primary" data-m="yes">${esc(t('runningConfirm'))}</button>
          </div></div>`;
      m.hidden = false;
      m.querySelector('[data-m="yes"]').focus();
      m.onclick = (ev) => {
        const b = ev.target.closest('[data-m]');
        if (!b) return;
        m.hidden = true;
        m.innerHTML = '';
        resolve(b.dataset.m === 'yes');
      };
    });
  }

  async function install() {
    const req = { scope: S.scope, dir: S.check ? S.check.dir : S.dir, options: S.scope === 'portable' ? {} : S.options };
    if (!(await confirmRunning(req.dir))) return;
    S.progress = { step: S.scope === 'machine' && !S.info.elevated ? 'elevate' : 'prepare', percent: 0, file: '' };
    S.busy = true;
    S.page = 'progress';
    render();
    try {
      await api.Install(req, true);
      S.progress = { step: 'finish', percent: 100, file: '' };
      paintProgress();
      await new Promise((r) => setTimeout(r, 450));
      S.dir = req.dir;
      S.page = 'done';
    } catch (e) {
      S.error = e;
      S.page = 'error';
    }
    S.busy = false;
    render();
  }

  async function uninstall() {
    const u = S.info.uninstall || {};
    if (!(await confirmRunning(u.dir))) return;
    S.progress = { step: u.scope === 'machine' && !S.info.elevated ? 'elevate' : 'remove', percent: 0, file: '' };
    S.busy = true;
    S.page = 'progress';
    render();
    try {
      await api.Uninstall(S.keepData, true);
      S.progress = { step: 'finish', percent: 100, file: '' };
      paintProgress();
      await new Promise((r) => setTimeout(r, 450));
      S.page = 'done';
    } catch (e) {
      S.error = e;
      S.page = 'error';
    }
    S.busy = false;
    render();
  }

  async function onAction(el) {
    const a = el.dataset.action;
    const list = pages();
    const i = list.indexOf(S.page);
    switch (a) {
      case 'minimise':
        return api.Minimise();
      case 'quit':
        if (!S.busy) return api.Quit();
        return;
      case 'lang':
        lang = el.dataset.lang;
        api.SetLanguage(lang);
        return render();
      case 'license':
        return api.OpenURL('https://www.gnu.org/licenses/agpl-3.0.html');
      case 'scope':
        return chooseScope(el.dataset.scope);
      case 'next':
        return go(list[i + 1]);
      case 'back':
        return go(list[i - 1]);
      case 'browse': {
        const picked = await api.Browse(S.dir);
        if (picked) {
          S.dir = picked;
          S.dirs[S.scope] = picked;
          await go('dir');
        }
        return;
      }
      case 'opt':
        S.options[el.dataset.opt] = !S.options[el.dataset.opt];
        el.setAttribute('aria-checked', String(S.options[el.dataset.opt]));
        return;
      case 'keepdata':
        S.keepData = !S.keepData;
        return render();
      case 'install':
        if (S.page === 'dir' && S.dir) S.dirs[S.scope] = S.dir;
        return install();
      case 'uninstall':
        return uninstall();
      case 'retry':
        S.page = isUninstall() ? 'uninstall' : S.scope === 'portable' ? 'dir' : 'options';
        if (S.page === 'dir') S.check = await api.CheckDir(S.scope, S.dir);
        return render();
      case 'launch':
        try {
          await api.Launch();
        } catch (e) {
          /* shown nowhere: TuxMail may still start */
        }
        return api.Quit();
      case 'defaultapps':
        return api.OpenDefaultApps();
      case 'openfolder':
        return api.OpenFolder(S.dir);
    }
  }

  document.addEventListener('click', (ev) => {
    const el = ev.target.closest('[data-action]');
    if (!el || el.disabled) return;
    ev.preventDefault();
    onAction(el);
  });
  document.addEventListener('keydown', (ev) => {
    if (ev.key === 'Enter' && ev.target.id === 'dir') {
      const b = document.querySelector('.footer .btn-primary:not([disabled])');
      if (b) b.click();
    }
    if (ev.key === 'Escape' && !$('#modal').hidden) {
      const no = document.querySelector('#modal [data-m="no"]');
      if (no) no.click();
    }
  });
  document.addEventListener('contextmenu', (ev) => {
    if (ev.target.tagName !== 'INPUT') ev.preventDefault();
  });

  // ------------------------------------------------------------ start
  async function start() {
    api = await bridge();
    const info = await api.State();
    S.info = info;
    lang = info.lang === 'de' ? 'de' : 'en';
    const root = document.documentElement;
    const dark = matchMedia('(prefers-color-scheme: dark)');
    const applyTheme = () => root.classList.toggle('dark', dark.matches || !!info.dark);
    applyTheme();
    dark.addEventListener('change', applyTheme);
    root.classList.toggle('mica', !!info.mica);
    S.dirs = { ...(info.dirs || {}) };
    const pre = info.preset;
    const ex = (info.existing || [])[0];
    S.scope = (pre && pre.scope) || (ex && ex.scope) || 'user';
    S.dir = (pre && pre.dir) || S.dirs[S.scope] || '';
    if (pre && pre.options) S.options = { ...pre.options };
    else if (ex && ex.options) S.options = { ...ex.options };
    S.page = info.mode === 'uninstall' ? 'uninstall' : 'welcome';
    if (window.runtime && window.runtime.EventsOn) {
      window.runtime.EventsOn('setup:progress', (p) => {
        if (!p) return;
        // Never go backwards (elevated child and in-process steps mix).
        if (p.percent < S.progress.percent && p.step === S.progress.step) return;
        S.progress = { step: p.step, percent: Math.max(p.percent, S.progress.percent), file: p.file || '' };
        if (S.page === 'progress') paintProgress();
      });
    }
    render();
  }
  start().catch((e) => {
    document.getElementById('content').innerHTML = `<div class="notice error">${esc(e.message || e)}</div>`;
  });
})();
