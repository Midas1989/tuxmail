-- Security (audit NC-01): signer certificates used to be collected from every
-- cryptographically valid S/MIME signature, including self-signed ones and
-- ones issued for a different sender, and were then preferred for encryption.
-- Only certificates of signatures that chain to a trusted root and match the
-- sender are collected from now on. Certificates collected earlier were never
-- verified; they are only a cache and are rebuilt from verified mail.
DELETE FROM smime_certs WHERE source = 'collected' AND own = 0 AND trusted = 0;
