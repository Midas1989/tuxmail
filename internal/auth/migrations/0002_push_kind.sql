-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Push subscription kind (webpush | unifiedpush | fcm), see mobile/SERVER_TODO.md.
ALTER TABLE push_subscriptions ADD COLUMN kind TEXT NOT NULL DEFAULT 'webpush';
