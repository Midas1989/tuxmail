// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Vim-style key map of the terminal view (components/mail/TerminalView.svelte). Pure and
// testable: KeyParser turns key presses into actions, with count prefixes (5j, 12G) and two-key
// sequences (gg, dd, gi). The view decides what an action does in the pane that has the focus.
// Modelled on aerc and vim, so that the view can be used without a mouse.

export type Pane = 'folders' | 'list' | 'reader';

export type ActionName =
  | 'down' | 'up' | 'top' | 'bottom' | 'halfDown' | 'halfUp' | 'pageDown' | 'pageUp'
  | 'open' | 'back' | 'paneLeft' | 'paneRight' | 'cyclePane' | 'cyclePaneBack'
  | 'command' | 'search' | 'help' | 'refresh'
  | 'compose' | 'reply' | 'replyAll' | 'forward' | 'archive' | 'trash' | 'flag' | 'toggleRead' | 'move'
  | 'mark' | 'unmarkAll' | 'nextUnread' | 'prevUnread' | 'nextMessage' | 'prevMessage'
  | 'goto';

export interface Action {
  name: ActionName;
  /** The count typed before the key (default 1). */
  count: number;
  /** True when a count was typed (12G goes to line 12, G alone to the end). */
  hasCount: boolean;
  /** goto: the folder role (inbox, sent, drafts, archive, trash, junk). */
  arg?: string;
}

export interface KeyInput {
  key: string;
  ctrl?: boolean;
  shift?: boolean;
  alt?: boolean;
  meta?: boolean;
}

/** g + key: go to a folder role. */
export const GOTO_ROLES: Record<string, string> = { i: 'inbox', s: 'sent', d: 'drafts', a: 'archive', t: 'trash', j: 'junk' };

const MODIFIER_KEYS = new Set(['Shift', 'Control', 'Alt', 'Meta', 'AltGraph', 'CapsLock']);

const SINGLE: Record<string, ActionName> = {
  j: 'down', ArrowDown: 'down',
  k: 'up', ArrowUp: 'up',
  G: 'bottom', End: 'bottom', Home: 'top',
  h: 'paneLeft', ArrowLeft: 'paneLeft',
  l: 'paneRight', ArrowRight: 'paneRight',
  Enter: 'open', o: 'open',
  Escape: 'back', q: 'back', Backspace: 'back',
  ':': 'command', '/': 'search', '?': 'help',
  c: 'compose', r: 'reply', R: 'replyAll', f: 'forward',
  e: 'archive', '#': 'trash', Delete: 'trash',
  s: 'flag', u: 'toggleRead', m: 'move',
  x: 'mark', X: 'unmarkAll',
  n: 'nextUnread', N: 'prevUnread',
  J: 'nextMessage', K: 'prevMessage',
  ' ': 'pageDown', PageDown: 'pageDown', PageUp: 'pageUp',
};

const CTRL: Record<string, ActionName> = { d: 'halfDown', u: 'halfUp', f: 'pageDown', b: 'pageUp', l: 'refresh' };

export class KeyParser {
  private digits = '';
  private prefix: '' | 'g' | 'd' = '';

  /** What has been typed and is still waiting for its end ("12g"): shown in the status line. */
  get pending(): string {
    return this.digits + this.prefix;
  }

  reset(): void {
    this.digits = '';
    this.prefix = '';
  }

  private done(name: ActionName, arg?: string): Action {
    const hasCount = this.digits !== '';
    const count = hasCount ? Math.min(Number(this.digits), 100000) : 1;
    this.reset();
    return { name, count, hasCount, arg };
  }

  /**
   * Feeds one key press. Returns the finished action, 'pending' while a count or sequence is
   * incomplete, or null when the key means nothing here (the typed prefix is dropped).
   */
  feed(k: KeyInput): Action | 'pending' | null {
    if (MODIFIER_KEYS.has(k.key)) return 'pending'; // pressing Shift for G must not end "12"
    if (k.alt || k.meta) {
      this.reset();
      return null;
    }
    if (k.ctrl) {
      const name = CTRL[k.key.toLowerCase()];
      if (!name) {
        this.reset();
        return null;
      }
      return this.done(name);
    }
    if (k.key === 'Escape') {
      const had = this.pending !== '';
      this.reset();
      return had ? null : { name: 'back', count: 1, hasCount: false };
    }
    if (this.prefix === 'g') {
      if (k.key === 'g') return this.done('top');
      if (k.key === 'r') return this.done('refresh');
      const role = GOTO_ROLES[k.key];
      if (role) return this.done('goto', role);
      this.reset();
      return null;
    }
    if (this.prefix === 'd') {
      if (k.key === 'd') return this.done('trash');
      this.reset();
      return null;
    }
    if (/^[0-9]$/.test(k.key) && (k.key !== '0' || this.digits !== '')) {
      this.digits += k.key;
      return 'pending';
    }
    if (k.key === 'g' || k.key === 'd') {
      this.prefix = k.key;
      return 'pending';
    }
    if (k.key === 'Tab') return this.done(k.shift ? 'cyclePaneBack' : 'cyclePane');
    const name = SINGLE[k.key];
    if (!name) {
      this.reset();
      return null;
    }
    return this.done(name);
  }
}
