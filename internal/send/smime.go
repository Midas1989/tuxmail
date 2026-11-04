// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"

	"github.com/Midas1989/Mailclient/internal/crypto/smime"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
)

// S/MIME errors.
var (
	ErrSMIMEUnavailable    = errors.New("send: S/MIME is not configured")
	ErrNoSMIMECertificate  = errors.New("send: no S/MIME certificate for the sender identity")
	ErrSMIMECannotEncrypt  = errors.New("send: not all recipients have a usable S/MIME certificate")
	ErrSMIMEAndPGPConflict = errors.New("send: S/MIME and OpenPGP cannot both be forced on")
)

// SMIMEKeys resolves S/MIME identities and certificates (implemented by the
// app on top of the secret store and the certificate table).
type SMIMEKeys interface {
	// SMIMEIdentity returns the own identity of an address (nil if none).
	SMIMEIdentity(ctx context.Context, email string) (*smime.Identity, error)
	// SMIMECertificates returns usable encryption certificates by address;
	// addresses without one are missing from the map.
	SMIMECertificates(ctx context.Context, emails []string) (map[string]*x509.Certificate, error)
}

// SMIMEOptions configure S/MIME for one send. The modes mean the same as
// for PGP; PGPAuto encrypts when every recipient has a certificate and the
// message is not OpenPGP-encrypted, and signs when encrypting.
type SMIMEOptions struct {
	Sign    PGPMode `json:"sign,omitempty"`
	Encrypt PGPMode `json:"encrypt,omitempty"`
	// SignAlways makes PGPAuto signing sign whenever the sender has a
	// certificate (not only when encrypting).
	SignAlways bool `json:"signAlways,omitempty"`
}

type smimePlan struct {
	sign, encrypt bool
	id            *smime.Identity
	recipients    []*x509.Certificate
}

func (p smimePlan) active() bool { return p.sign || p.encrypt }

// planSMIME decides whether to sign/encrypt with S/MIME. Automatic
// encryption only applies when S/MIME was not explicitly turned off and
// OpenPGP is not forced.
func (s *Service) planSMIME(ctx context.Context, req *Request) (smimePlan, error) {
	var p smimePlan
	o := req.SMIME
	if o.Sign == PGPOn || o.Encrypt == PGPOn {
		if req.PGP.Sign == PGPOn || req.PGP.Encrypt == PGPOn {
			return p, ErrSMIMEAndPGPConflict
		}
	}
	if s.cfg.SMIME == nil {
		if o.Sign == PGPOn || o.Encrypt == PGPOn {
			return p, ErrSMIMEUnavailable
		}
		return p, nil
	}
	if o.Sign == PGPOff && o.Encrypt == PGPOff {
		return p, nil
	}
	d := &req.Draft
	id, err := s.cfg.SMIME.SMIMEIdentity(ctx, senderEmail(d))
	if err != nil && (o.Sign == PGPOn || o.Encrypt == PGPOn) {
		return p, fmt.Errorf("%w: %v", ErrNoSMIMECertificate, err)
	}
	p.id = id
	pgpForced := req.PGP.Sign == PGPOn || req.PGP.Encrypt == PGPOn
	if o.Encrypt == PGPOn || (o.Encrypt == PGPAuto && !pgpForced && id != nil) {
		rcpts := allRecipients(d)
		certs, err := s.cfg.SMIME.SMIMECertificates(ctx, rcpts)
		if err != nil {
			return p, err
		}
		var missing []string
		var list []*x509.Certificate
		for _, r := range rcpts {
			c := certs[strings.ToLower(r)]
			if c == nil {
				missing = append(missing, r)
				continue
			}
			list = append(list, c)
		}
		switch {
		case len(missing) == 0 && len(list) > 0:
			p.encrypt, p.recipients = true, list
		case o.Encrypt == PGPOn:
			return p, fmt.Errorf("%w: %s", ErrSMIMECannotEncrypt, strings.Join(missing, ", "))
		}
	}
	switch o.Sign {
	case PGPOn:
		if id == nil {
			return p, ErrNoSMIMECertificate
		}
		p.sign = true
	case PGPAuto:
		p.sign = (p.encrypt || o.SignAlways) && id != nil
	}
	if p.encrypt && id != nil {
		p.recipients = append(p.recipients, id.Cert) // readable Sent copy
	}
	return p, nil
}

// protectSMIME signs and/or encrypts the composed message in place.
func (s *Service) protectSMIME(msg *compose.Message, p smimePlan, res *Result) error {
	if !p.active() {
		return nil
	}
	var out []byte
	var err error
	switch {
	case p.encrypt && p.sign:
		out, err = smime.SignAndEncrypt(msg.Body, p.id, p.recipients)
	case p.encrypt:
		out, err = smime.Encrypt(msg.Body, p.recipients)
	default:
		out, err = smime.Sign(msg.Body, p.id)
	}
	if err != nil {
		return fmt.Errorf("send: S/MIME: %w", err)
	}
	msg.Body = out
	res.Encrypted, res.Signed = p.encrypt, p.sign
	res.SMIME = true
	return nil
}
