-- «Двухэтапная авторизация» (GO24DEV-757): the last sign-in with a second factor (amr «mfa» of Authentik's ID token);
-- the policy itself is the company setting «two_factor».
ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_at timestamptz;
