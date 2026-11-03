// SPDX-License-Identifier: AGPL-3.0-or-later
import { createApiFromWindow } from '../api/select';
import { AppState } from './app.svelte';

/** The application-wide state singleton. */
export const app = new AppState(createApiFromWindow());

export { prefs } from './prefs.svelte';
export type { Route, ViewMode, SidebarSel, ComposerInit, ChannelOption } from './app.svelte';
