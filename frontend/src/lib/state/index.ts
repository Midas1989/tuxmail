// SPDX-License-Identifier: AGPL-3.0-or-later
import { createApiFromWindow } from '../api/select';
import { AppState } from './app.svelte';
import { SearchState } from './search.svelte';

/** The application-wide state singleton. */
export const app = new AppState(createApiFromWindow());
export const search = new SearchState(app.api);

export { prefs } from './prefs.svelte';
export type { Route, ViewMode, SidebarSel, ComposerInit, ChannelOption } from './app.svelte';

// --- Cloud ---
import { CloudState } from './cloud.svelte';
export const cloud = new CloudState(app.api);
