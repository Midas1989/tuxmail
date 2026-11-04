// SPDX-License-Identifier: AGPL-3.0-or-later
import { createApiFromWindow } from '../api/select';
import { AppState } from './app.svelte';
import { SearchState } from './search.svelte';
import { createSwitchableApi } from '../api/team';
import { TeamState } from './team.svelte';
import { isNativeShell } from '../platform';
import { nativeHttpOptions } from '../native/session';

// The root transport; mailbox methods switch to a team mailbox while one is open (state/team.svelte.ts).
const rootApi = createApiFromWindow();
const workspace = createSwitchableApi(rootApi);

/** The application-wide state singleton. */
export const app = new AppState(workspace.api);
export const search = new SearchState(app.api);

export { prefs } from './prefs.svelte';
export type { Route, ViewMode, SidebarSel, ComposerInit, ChannelOption } from './app.svelte';

// --- Cloud ---
import { CloudState } from './cloud.svelte';
export const cloud = new CloudState(app.api);

// --- Team mailboxes (server mode) ---
export const team = new TeamState(rootApi, workspace, app, isNativeShell() ? nativeHttpOptions() : {});
