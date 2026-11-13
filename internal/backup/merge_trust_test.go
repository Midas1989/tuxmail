package backup

import (
	"bytes"
	"context"
	"testing"
)

// M-5: a merge must neither grant trust nor import own keys / private keys.
func TestMergeGrantsNoKeyTrust(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	db := e.st.DB()
	for _, q := range []string{
		`INSERT INTO smime_certs(fingerprint,email,der,own,trusted,source,secret_ref,created_at) VALUES ('c-trusted','a@x.org',x'00',0,1,'imported',NULL,1)`,
		`INSERT INTO smime_certs(fingerprint,email,der,own,trusted,source,secret_ref,created_at) VALUES ('c-own','me@x.org',x'00',1,1,'own','smime:c-own',1)`,
		`INSERT INTO pgp_keys(fingerprint,email,public_key,source,secret_ref,created_at,updated_at) VALUES ('K-VERIFIED','a@x.org',x'00','manual',NULL,1,1)`,
		`INSERT INTO pgp_keys(fingerprint,email,public_key,source,secret_ref,created_at,updated_at) VALUES ('K-OWN','me@x.org',x'00','own','pgp:K-OWN',1,1)`,
		`INSERT INTO pgp_trust(address,fingerprint,level,source,verified_at,seen_count) VALUES ('a@x.org','K-VERIFIED','verified','manual',5,3)`,
		`INSERT INTO pgp_trust(address,fingerprint,level,source,seen_count) VALUES ('me@x.org','K-OWN','verified','own',1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	e.sec.Set("pgp:K-OWN", []byte("private"))
	e.sec.Set("smime:c-own", []byte("p12"))
	var buf bytes.Buffer
	if _, err := Create(ctx, &buf, Options{Store: e.st, Blobs: e.blobs, Secrets: e.sec, SecretRefs: []string{"pgp:K-OWN", "smime:c-own"},
		Scope: Scope{Mode: ModeFull, Secrets: true}, Passphrase: "pw", TempDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	arc := buf.Bytes()

	other := newEnv(t)
	ex, err := Extract(ctx, src(arc, "a"), "pw", nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	res, err := Restore(ctx, ex, RestoreOptions{Mode: Merge, Secrets: true, Store: other.st, Blobs: other.blobs, SecretStore: other.sec})
	if err != nil {
		t.Fatal(err)
	}
	var trusted, own int
	var source string
	if err := other.st.DB().QueryRowContext(ctx, `SELECT trusted, own, source FROM smime_certs WHERE fingerprint='c-trusted'`).Scan(&trusted, &own, &source); err != nil {
		t.Fatalf("cert not merged: %v", err)
	}
	if trusted != 0 || own != 0 || source != "backup" {
		t.Errorf("cert: trusted=%d own=%d source=%q", trusted, own, source)
	}
	var n int
	other.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM smime_certs WHERE fingerprint='c-own'`).Scan(&n)
	if n != 0 {
		t.Error("own certificate merged")
	}
	other.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pgp_keys WHERE fingerprint='K-OWN'`).Scan(&n)
	if n != 0 {
		t.Error("own PGP key merged")
	}
	if err := other.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pgp_keys WHERE fingerprint='K-VERIFIED'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("public key not merged: %d %v", n, err)
	}
	var level string
	var verified *int64
	if err := other.st.DB().QueryRowContext(ctx, `SELECT level, verified_at FROM pgp_trust WHERE fingerprint='K-VERIFIED'`).Scan(&level, &verified); err != nil {
		t.Fatal(err)
	}
	if level != "unknown" || verified != nil {
		t.Errorf("pgp trust: level=%q verified_at=%v", level, verified)
	}
	other.st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pgp_trust WHERE fingerprint='K-OWN'`).Scan(&n)
	if n != 0 {
		t.Error("trust of own key merged")
	}
	for _, ref := range []string{"pgp:K-OWN", "smime:c-own"} {
		if _, err := other.sec.Get(ref); err == nil {
			t.Errorf("private key %s imported by merge", ref)
		}
	}
	if res.KeysUntrusted != 2 || res.KeysSkipped < 5 {
		t.Errorf("report: untrusted=%d skipped=%d", res.KeysUntrusted, res.KeysSkipped)
	}
}
