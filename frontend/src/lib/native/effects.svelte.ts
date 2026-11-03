// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Midas Wollinger and TuxMail contributors
//
// Reactive glue between the UI state and native features (runes outside
// components via $effect.root):
//  - system bar icon colour follows the effective theme (light/dark),
//  - opening a conversation removes its notifications.

import { app, prefs } from '../state';

export interface EffectDeps {
  setBarStyle: (dark: boolean) => void;
  cancelConversation: (conversationId: number) => void;
}

/** Dark UI -> light status bar icons (SystemBarsStyle.Dark) and vice versa. */
export function barStyleForTheme(theme: string): 'DARK' | 'LIGHT' {
  return theme === 'dark' ? 'DARK' : 'LIGHT';
}

export function startNativeEffects(deps: EffectDeps): () => void {
  return $effect.root(() => {
    $effect(() => {
      deps.setBarStyle(prefs.effectiveTheme === 'dark');
    });
    $effect(() => {
      const id = app.selectedConversationId;
      if (id != null) deps.cancelConversation(id);
    });
  });
}
