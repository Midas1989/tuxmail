// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
import { describe, expect, it } from 'vitest';
import { BACK_EVENT, decideBack, dispatchBackEvent, parseConnectLink, parseDeepLink, type BackState } from './navigation';

const base: BackState = { handledByUi: false, dialogOpen: false, replying: false, infoOpen: false, sidebarOpen: false, detailOpen: false, route: 'mail' };

describe('decideBack', () => {
  it('closes the innermost layer first', () => {
    expect(decideBack({ ...base, dialogOpen: true, detailOpen: true, route: 'calendar' })).toBe('close-dialog');
    expect(decideBack({ ...base, replying: true, detailOpen: true })).toBe('cancel-reply');
    expect(decideBack({ ...base, infoOpen: true, detailOpen: true })).toBe('close-info');
    expect(decideBack({ ...base, sidebarOpen: true })).toBe('close-sidebar');
    expect(decideBack({ ...base, detailOpen: true })).toBe('close-conversation');
    expect(decideBack({ ...base, route: 'settings' })).toBe('go-mail');
    expect(decideBack(base)).toBe('minimize');
  });
  it('lets UI components take over', () => {
    expect(decideBack({ ...base, handledByUi: true, dialogOpen: true })).toBe('handled');
  });
  it('dispatches a cancelable event', () => {
    const t = new EventTarget();
    expect(dispatchBackEvent(t)).toBe(false);
    t.addEventListener(BACK_EVENT, (e) => e.preventDefault());
    expect(dispatchBackEvent(t)).toBe(true);
  });
});

describe('parseDeepLink', () => {
  it('handles notification taps', () => {
    expect(parseDeepLink('tuxmail://open?url=' + encodeURIComponent('./#/mail?view=chat') + '&conversationId=42')).toEqual({ hash: '#/mail?view=chat', conversationId: 42 });
    expect(parseDeepLink('tuxmail://open?conversationId=7')).toEqual({ hash: '#/mail?view=chat', conversationId: 7 });
    expect(parseDeepLink('tuxmail://open?url=' + encodeURIComponent('/#/calendar'))).toEqual({ hash: '#/calendar' });
  });
  it('handles short forms', () => {
    expect(parseDeepLink('tuxmail://conversation/12')).toEqual({ hash: '#/mail?view=chat', conversationId: 12 });
    expect(parseDeepLink('tuxmail://message/34')).toEqual({ hash: '#/mail', messageId: 34 });
    expect(parseDeepLink('tuxmail://compose')).toEqual({ hash: '#/mail', compose: true });
    expect(parseDeepLink('tuxmail://conversation/abc')).toEqual({ hash: '#/mail' });
  });
  it('maps hash routes and query ids from web URLs', () => {
    expect(parseDeepLink('https://mail.example.org/#/mail?conversation=5')).toEqual({ hash: '#/mail?conversation=5', conversationId: 5 });
    expect(parseDeepLink('./#/settings')).toEqual({ hash: '#/settings' });
    expect(parseDeepLink('https://x/#/evil-route')).toEqual({ hash: '#/mail' });
    expect(parseDeepLink('https://x/')).toEqual({ hash: '#/mail' });
  });
  it('ignores other schemes and connect links', () => {
    expect(parseDeepLink('javascript:alert(1)')).toBeNull();
    expect(parseDeepLink('tuxmail://connect?server=https://a')).toBeNull();
  });
});

describe('parseConnectLink', () => {
  it('extracts the server', () => {
    expect(parseConnectLink('tuxmail://connect?server=' + encodeURIComponent('https://mail.example.org'))).toEqual({ server: 'https://mail.example.org' });
    expect(parseConnectLink('tuxmail://connect')).toBeNull();
    expect(parseConnectLink('https://connect?server=x')).toBeNull();
  });
});
