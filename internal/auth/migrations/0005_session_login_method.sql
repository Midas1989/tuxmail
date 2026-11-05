-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Re-audit 2026-11-05, NA-02: remember how a session was created. The
-- exemption of OIDC users from "totp_required_for_admins" (their second
-- factor is the identity provider's) only holds for sessions the OIDC
-- callback created, not for a password login of an OIDC-linked account.
-- Existing sessions default to 'password' (no exemption until the next
-- OIDC login) - the conservative choice.
ALTER TABLE sessions ADD COLUMN login_method TEXT NOT NULL DEFAULT 'password'
    CHECK (login_method IN ('password','oidc','device_code'));
