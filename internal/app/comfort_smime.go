// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/crypto/smime"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
	"github.com/Midas1989/Mailclient/internal/store"
)

// SMIMECertificate is a certificate in the certificate manager.
type SMIMECertificate struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	Subject     string `json:"subject"`
	Issuer      string `json:"issuer"`
	Fingerprint string `json:"fingerprint"`
	NotBefore   string `json:"notBefore"`
	NotAfter    string `json:"notAfter"`
	Own         bool   `json:"own"`     // with private key
	Trusted     bool   `json:"trusted"` // approved by the user
	Source      string `json:"source"`  // own | imported | collected
	Expired     bool   `json:"expired"`
	// CanEncrypt: usable as an encryption target now.
	CanEncrypt bool `json:"canEncrypt"`
}

// SMIMEImportInput imports a PKCS#12 file (own certificate) or a
// certificate of somebody else (PEM or DER, Password empty).
type SMIMEImportInput struct {
	Data     string `json:"data"` // base64 file content
	Password string `json:"password,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// SMIMEStatus tells the composer what is possible for a draft.
type SMIMEStatus struct {
	CanSign    bool     `json:"canSign"`
	CanEncrypt bool     `json:"canEncrypt"`
	Missing    []string `json:"missing,omitempty"`
}

type smimeSecret struct {
	Key   string   `json:"key"`   // PKCS#8, base64
	Chain []string `json:"chain"` // DER, base64 (end-entity first)
}

func certInfo(c store.SMIMECert, now time.Time) SMIMECertificate {
	out := SMIMECertificate{ID: c.ID, Email: c.Email, Subject: c.Subject, Issuer: c.Issuer, Fingerprint: c.Fingerprint,
		NotBefore: c.NotBefore.Format(time.RFC3339), NotAfter: c.NotAfter.Format(time.RFC3339), Own: c.Own, Trusted: c.Trusted,
		Source: c.Source, Expired: now.After(c.NotAfter)}
	if x, err := x509.ParseCertificate(c.DER); err == nil {
		out.CanEncrypt = smime.CanEncryptTo(x, now)
	}
	return out
}

// ListSMIMECertificates returns all certificates.
func (a *App) ListSMIMECertificates(ctx context.Context) ([]SMIMECertificate, error) {
	cs, err := a.st.ListSMIMECerts(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]SMIMECertificate, 0, len(cs))
	for _, c := range cs {
		out = append(out, certInfo(c, a.now()))
	}
	return out, nil
}

func storeCert(c *x509.Certificate) store.SMIMECert {
	email := ""
	if em := smime.Emails(c); len(em) > 0 {
		email = em[0]
	}
	return store.SMIMECert{Fingerprint: smime.Fingerprint(c), Email: email, Subject: c.Subject.CommonName, Issuer: c.Issuer.CommonName,
		NotBefore: c.NotBefore, NotAfter: c.NotAfter, DER: c.Raw}
}

// ImportSMIME imports a certificate: PKCS#12 with password (own identity,
// key into the secret store) or a PEM/DER certificate of somebody else.
func (a *App) ImportSMIME(ctx context.Context, in SMIMEImportInput) (SMIMECertificate, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.Data))
	if err != nil {
		return SMIMECertificate{}, invalid("data must be base64")
	}
	lower := strings.ToLower(in.Filename)
	isP12 := strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx") || in.Password != ""
	if !isP12 {
		var der []byte
		if b, _ := pem.Decode(data); b != nil {
			der = b.Bytes
		} else {
			der = data
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			// maybe a PKCS#12 without password
			isP12 = true
		} else {
			sc := storeCert(c)
			sc.Source, sc.Trusted = "imported", true
			id, err := a.st.PutSMIMECert(ctx, sc)
			if err != nil {
				return SMIMECertificate{}, err
			}
			sc.ID = id
			return certInfo(sc, a.now()), nil
		}
	}
	id, err := smime.ImportPKCS12(data, in.Password)
	if err != nil {
		return SMIMECertificate{}, &Error{Code: "smime_import", Status: 400, Message: "the certificate file could not be read (wrong password?)", Err: err}
	}
	key, err := x509.MarshalPKCS8PrivateKey(id.Key)
	if err != nil {
		return SMIMECertificate{}, err
	}
	sec := smimeSecret{Key: base64.StdEncoding.EncodeToString(key)}
	for _, c := range append([]*x509.Certificate{id.Cert}, id.Chain...) {
		sec.Chain = append(sec.Chain, base64.StdEncoding.EncodeToString(c.Raw))
	}
	raw, _ := json.Marshal(sec)
	sc := storeCert(id.Cert)
	ref := "smime:" + sc.Fingerprint
	if err := a.opts.Secrets.Set(ref, raw); err != nil {
		return SMIMECertificate{}, err
	}
	sc.Own, sc.Trusted, sc.Source, sc.SecretRef = true, true, "own", ref
	cid, err := a.st.PutSMIMECert(ctx, sc)
	if err != nil {
		return SMIMECertificate{}, err
	}
	sc.ID = cid
	return certInfo(sc, a.now()), nil
}

// SetSMIMETrusted approves (or revokes approval of) a certificate.
func (a *App) SetSMIMETrusted(ctx context.Context, id int64, trusted bool) error {
	return a.st.SetSMIMETrusted(ctx, id, trusted)
}

// DeleteSMIMECertificate removes a certificate (and an own private key).
func (a *App) DeleteSMIMECertificate(ctx context.Context, id int64) error {
	c, err := a.st.GetSMIMECert(ctx, id)
	if err != nil {
		return err
	}
	if c.SecretRef != "" {
		_ = a.opts.Secrets.Delete(c.SecretRef)
	}
	return a.st.DeleteSMIMECert(ctx, id)
}

// SMIMEStatusFor reports what S/MIME can do for a sender and recipients.
func (a *App) SMIMEStatusFor(ctx context.Context, from string, recipients []string) (SMIMEStatus, error) {
	k := smimeKeys{a: a}
	var st SMIMEStatus
	if id, err := k.SMIMEIdentity(ctx, from); err == nil && id != nil {
		st.CanSign = true
	}
	certs, err := k.SMIMECertificates(ctx, recipients)
	if err != nil {
		return st, err
	}
	for _, r := range recipients {
		if certs[strings.ToLower(r)] == nil {
			st.Missing = append(st.Missing, r)
		}
	}
	st.CanEncrypt = st.CanSign && len(st.Missing) == 0 && len(recipients) > 0
	return st, nil
}

// smimeKeys implements send.SMIMEKeys.
type smimeKeys struct{ a *App }

func (k smimeKeys) SMIMEIdentity(ctx context.Context, email string) (*smime.Identity, error) {
	ids, err := k.a.smimeIdentities(ctx, email)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	return ids[0], nil
}

func (k smimeKeys) SMIMECertificates(ctx context.Context, emails []string) (map[string]*x509.Certificate, error) {
	out := map[string]*x509.Certificate{}
	now := k.a.now()
	for _, e := range emails {
		cs, err := k.a.st.ListSMIMECerts(ctx, e)
		if err != nil {
			return nil, err
		}
		// Certificates the user imported or trusts always win over ones that
		// were merely collected from incoming mail; within a class the one
		// valid the longest is used (audit NC-01).
		var best *x509.Certificate
		bestRank := -1
		for _, c := range cs {
			x, err := x509.ParseCertificate(c.DER)
			if err != nil || !smime.CanEncryptTo(x, now) {
				continue
			}
			rank := 0
			if c.Own || c.Trusted || c.Source == "imported" {
				rank = 1
			}
			if best == nil || rank > bestRank || (rank == bestRank && x.NotAfter.After(best.NotAfter)) {
				best, bestRank = x, rank
			}
		}
		if best != nil {
			out[strings.ToLower(e)] = best
		}
	}
	return out, nil
}

// smimeIdentities loads own identities ("" = all), valid ones first.
func (a *App) smimeIdentities(ctx context.Context, email string) ([]*smime.Identity, error) {
	cs, err := a.st.ListSMIMECerts(ctx, email)
	if err != nil {
		return nil, err
	}
	now := a.now()
	var valid, expired []*smime.Identity
	for _, c := range cs {
		if !c.Own || c.SecretRef == "" {
			continue
		}
		id, err := a.loadSMIMEIdentity(c)
		if err != nil {
			a.log.Info("comfort: S/MIME key unavailable", "cert", c.Fingerprint, "err", err)
			continue
		}
		if now.After(id.Cert.NotAfter) {
			expired = append(expired, id)
		} else {
			valid = append(valid, id)
		}
	}
	return append(valid, expired...), nil
}

func (a *App) loadSMIMEIdentity(c store.SMIMECert) (*smime.Identity, error) {
	cs := a.comfort
	cs.smimeMu.Lock()
	if v, ok := cs.smimeCache[c.Fingerprint].(*smime.Identity); ok {
		cs.smimeMu.Unlock()
		return v, nil
	}
	cs.smimeMu.Unlock()
	raw, err := a.opts.Secrets.Get(c.SecretRef)
	if err != nil {
		return nil, err
	}
	var sec smimeSecret
	if err := json.Unmarshal(raw, &sec); err != nil {
		return nil, err
	}
	kb, err := base64.StdEncoding.DecodeString(sec.Key)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(kb)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("unsupported key type %T", key)
	}
	id := &smime.Identity{Key: signer}
	for i, s := range sec.Chain {
		der, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
		x, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			id.Cert = x
		} else {
			id.Chain = append(id.Chain, x)
		}
	}
	if id.Cert == nil {
		return nil, fmt.Errorf("no certificate")
	}
	cs.smimeMu.Lock()
	cs.smimeCache[c.Fingerprint] = id
	cs.smimeMu.Unlock()
	return id, nil
}

// smimeReader implements parse.SMIMEReader for display and analysis. With
// collect set, signer certificates are stored for later encryption.
type smimeReader struct {
	a       *App
	collect bool
}

func (r smimeReader) ReadSMIME(ctx context.Context, raw []byte) (*parse.SMIMEResult, error) {
	a := r.a
	ids, _ := a.smimeIdentities(ctx, "")
	var trusted []*x509.Certificate
	if cs, err := a.st.ListSMIMECerts(ctx, ""); err == nil {
		for _, c := range cs {
			if c.Trusted {
				if x, err := x509.ParseCertificate(c.DER); err == nil {
					trusted = append(trusted, x)
				}
			}
		}
	}
	from := ""
	if pm, err := parse.Parse(raw, parse.Options{SkipAttachmentData: true}); err == nil {
		from = fromEmail(pm)
	}
	res, err := smime.Read(raw, smime.ReadOptions{Identities: ids, Trusted: trusted, From: from, Now: a.now()})
	if res == nil {
		return nil, err
	}
	out := &parse.SMIMEResult{Content: res.Content, SMIMEInfo: parse.SMIMEInfo{Present: true, Signed: res.Signed, Encrypted: res.Encrypted,
		Decrypted: res.Decrypted, Signature: string(res.Signature), SignerEmail: res.SignerEmail, SignerName: res.SignerName,
		Issuer: res.SignerIssuer, Fingerprint: res.SignerFingerprint, Error: res.Error}}
	// Only certificates of signatures that chain to a trusted root and match
	// the sender are remembered for encryption (audit NC-01). Self-signed,
	// mismatching or expired signer certificates must never end up here.
	if r.collect && res.Signer != nil && res.Signature == smime.SigTrusted {
		sc := storeCert(res.Signer)
		sc.Source = "collected"
		if _, err := a.st.PutSMIMECert(ctx, sc); err != nil {
			a.log.Info("comfort: storing S/MIME certificate failed", "err", err)
		}
	}
	return out, err
}
