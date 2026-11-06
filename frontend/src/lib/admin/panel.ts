// SPDX-License-Identifier: AGPL-3.0-or-later

import type { AdminClient, AdminMe, AdminServerConfig } from '../api/admin';

/** What every tab of Settings -> Administration receives. */
export interface AdminPanelProps {
  client: AdminClient;
  /** The signed-in administrator. */
  me: AdminMe;
  /** Public server settings (password policy, invites); null while unknown. */
  config: AdminServerConfig | null;
  /** Toast. */
  notify: (text: string, kind?: 'info' | 'error') => void;
}
