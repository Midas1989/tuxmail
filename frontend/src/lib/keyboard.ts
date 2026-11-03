// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Global keyboard shortcuts (KONZEPT 16.2). Single-key shortcuts are ignored
// while typing or while a dialog is open; Ctrl+K and Ctrl+Shift+V work
// everywhere except inside text fields (where Ctrl+Shift+V pastes plain text).

import type { AppState } from './state/app.svelte';
import { prefs } from './state/prefs.svelte';

export function isEditable(el: EventTarget | null): boolean {
  if (!(el instanceof HTMLElement)) return false;
  if (el.isContentEditable) return true;
  const tag = el.tagName;
  if (tag === 'TEXTAREA' || tag === 'SELECT') return true;
  if (tag === 'INPUT') {
    const type = (el as HTMLInputElement).type;
    return !['checkbox', 'radio', 'button', 'submit', 'reset', 'range', 'color', 'file'].includes(type);
  }
  return false;
}

export function installShortcuts(app: AppState): () => void {
  const onKey = (e: KeyboardEvent) => {
    const mod = e.ctrlKey || e.metaKey;
    const editable = isEditable(e.target);
    const dialogOpen = !!document.querySelector('[aria-modal="true"]');

    if (e.key === 'Escape' && app.paletteOpen) {
      e.preventDefault();
      app.paletteOpen = false;
      return;
    }
    if (mod && !e.shiftKey && e.key.toLowerCase() === 'k') {
      e.preventDefault();
      app.paletteOpen = !app.paletteOpen;
      return;
    }
    if (mod && e.shiftKey && e.key.toLowerCase() === 'v' && !editable && !dialogOpen) {
      e.preventDefault();
      if (app.route !== 'mail') app.navigate('mail');
      void app.toggleViewMode();
      return;
    }
    if (editable || dialogOpen || mod || e.altKey) return;
    if (app.route !== 'mail') return;

    const classic = prefs.viewMode === 'classic';
    const msgId = app.selectedMessageId;
    switch (e.key) {
      case 'j':
        e.preventDefault();
        app.moveSelection(1);
        break;
      case 'k':
        e.preventDefault();
        app.moveSelection(-1);
        break;
      case 'Enter': {
        if (e.target instanceof HTMLButtonElement || e.target instanceof HTMLAnchorElement) return;
        e.preventDefault();
        if (classic) {
          if (msgId == null && app.messages.length) void app.selectMessage(app.messages[0].id, { scroll: true });
          else document.querySelector<HTMLElement>('.pane-detail iframe, .pane-detail [role="toolbar"] button')?.focus();
        } else if (app.selectedConversationId == null && app.conversations.length) {
          void app.selectConversation(app.conversations[0].id);
        } else {
          document.querySelector<HTMLElement>('[data-chat-input]')?.focus();
        }
        break;
      }
      case 'Escape':
        if (app.replyingTo) {
          app.replyingTo = null;
          break;
        }
        if (app.infoOpen && !classic) {
          app.infoOpen = false;
          break;
        }
        if (app.sidebarOpen) app.sidebarOpen = false;
        else if (classic && msgId != null) void app.selectMessage(null);
        else if (!classic && app.selectedConversationId != null) void app.selectConversation(null);
        document.querySelector<HTMLElement>(classic ? '[data-list="messages"]' : '[data-list="conversations"]')?.focus();
        break;
      case 'c':
        e.preventDefault();
        app.openCompose();
        break;
      case '/':
        e.preventDefault();
        document.querySelector<HTMLInputElement>('[data-search]')?.focus();
        break;
      case 'r':
      case 'R':
        if (classic && app.message) {
          e.preventDefault();
          void app.replyTo(e.shiftKey ? 'replyAll' : 'reply');
        }
        break;
      case 'f':
        if (classic && app.message) {
          e.preventDefault();
          void app.replyTo('forward');
        }
        break;
      case 'e':
        if (classic && msgId != null) void app.moveToRole(msgId, 'archive');
        break;
      case 'Delete':
      case '#':
        if (classic && msgId != null) void app.moveToRole(msgId, 'trash');
        break;
      case 's':
        if (classic && msgId != null) void app.toggleFlag(msgId);
        break;
      case 'u':
        if (classic && msgId != null) void app.toggleRead(msgId);
        break;
      case 't':
        e.preventDefault();
        app.tagSelection();
        break;
    }
  };
  window.addEventListener('keydown', onKey);
  return () => window.removeEventListener('keydown', onKey);
}
