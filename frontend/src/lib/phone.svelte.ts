// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Shared phone breakpoint (same as the "max-width: 639px" media queries of the CSS).

import { MediaQuery } from 'svelte/reactivity';

export const phone = new MediaQuery('(max-width: 639px)');
