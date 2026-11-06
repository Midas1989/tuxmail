// SPDX-License-Identifier: AGPL-3.0-or-later

package send

import (
	"context"
	"fmt"
	"strings"

	"github.com/Midas1989/Mailclient/internal/crypto/pgp"
	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/compose"
	"github.com/Midas1989/Mailclient/internal/mime/parse"
)

// pgpPlan is the resolved OpenPGP decision for one send.
type pgpPlan struct {
	encrypt, sign bool
	ownKey        *pgp.Key // unlocked own secret key (nil if none)
	recipients    []*pgp.Key
}

// PGPPrefs are the user's OpenPGP preferences.
type PGPPrefs struct {
	Prefer      pgp.PreferEncrypt
	NoAutocrypt bool
	// SignAlways makes PGPAuto signing sign whenever the sender has a key.
	SignAlways bool
}

func (s *Service) pgpPrefs() PGPPrefs {
	if s.cfg.PGPPrefs != nil {
		return s.cfg.PGPPrefs()
	}
	return PGPPrefs{}
}

func (s *Service) ownPrefer() pgp.PreferEncrypt {
	if p := s.pgpPrefs().Prefer; p != "" {
		return p
	}
	if s.cfg.PGP != nil && s.cfg.PGP.OwnPreferEncrypt != "" {
		return s.cfg.PGP.OwnPreferEncrypt
	}
	return pgp.PreferNoPreference
}

// ownSecretKey returns the unlocked secret key of the sender address.
func (s *Service) ownSecretKey(ctx context.Context, email string, unlock bool) (*pgp.Key, error) {
	ring := s.cfg.PGP.Ring
	if ring == nil {
		return nil, nil
	}
	keys, err := ring.FindByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	now := s.cfg.Now()
	for _, k := range keys {
		if k.IsSecret() && k.CanSign(now) {
			if !unlock {
				return k, nil
			}
			return ring.Unlock(ctx, k.Fingerprint())
		}
	}
	return nil, nil
}

// senderEmail is the address the signature should belong to.
func senderEmail(d *compose.Draft) string {
	switch {
	case d.OnBehalfOf != nil && d.OnBehalfOf.Email != "":
		return d.Identity.Email // signed by the actual sender
	case d.From != nil && d.From.Email != "":
		return d.From.Email
	}
	return d.Identity.Email
}

func allRecipients(d *compose.Draft) []string {
	var out []string
	for _, l := range [][]domain.Address{d.To, d.Cc, d.Bcc} {
		for _, a := range l {
			out = append(out, a.Email)
		}
	}
	return out
}

// repliesToEncrypted reports whether the draft answers an encrypted mail; the
// ciphertext is recognised without any key.
func repliesToEncrypted(d *compose.Draft) bool {
	raw := originalRaw(d)
	if d.Kind == compose.KindNew || len(raw) == 0 {
		return false
	}
	m, err := parse.Parse(raw, parse.Options{SkipAttachmentData: true})
	return err == nil && m.PGP.Encrypted
}

// planPGP decides whether to sign and encrypt.
func (s *Service) planPGP(ctx context.Context, req *Request, unlock bool) (pgpPlan, error) {
	var p pgpPlan
	o := req.PGP
	if s.cfg.PGP == nil {
		if o.Sign == PGPOn || o.Encrypt == PGPOn {
			return p, ErrPGPUnavailable
		}
		return p, nil
	}
	d := &req.Draft
	own, err := s.ownSecretKey(ctx, senderEmail(d), unlock)
	if err != nil {
		if o.Sign == PGPOn {
			return p, fmt.Errorf("%w: %v", ErrNoSigningKey, err)
		}
		own = nil
	}
	p.ownKey = own

	if o.Encrypt != PGPOff {
		planner := *s.cfg.PGP
		planner.OwnPreferEncrypt = s.ownPrefer()
		plan, err := planner.Plan(ctx, allRecipients(d), o.ReplyToEncrypted || repliesToEncrypted(d), s.cfg.Now())
		if err != nil {
			return p, err
		}
		switch o.Encrypt {
		case PGPOn:
			if !plan.CanEncrypt {
				return p, fmt.Errorf("%w: %s", ErrCannotEncrypt, strings.Join(plan.MissingKeys, ", "))
			}
			p.encrypt = true
		case PGPAuto:
			// Without an own key the copy in Sent would be unreadable.
			p.encrypt = own != nil && plan.CanEncrypt && plan.Recommendation == pgp.RecEncrypt
		}
		if p.encrypt {
			p.recipients = plan.Keys()
		}
	}
	switch o.Sign {
	case PGPOn:
		if own == nil {
			return p, ErrNoSigningKey
		}
		p.sign = true
	case PGPAuto:
		p.sign = (p.encrypt || s.pgpPrefs().SignAlways) && own != nil
	}
	return p, nil
}

// protect signs and/or encrypts the composed message in place.
func (s *Service) protect(_ context.Context, msg *compose.Message, plan pgpPlan, o PGPOptions, res *Result) error {
	if !plan.encrypt && !plan.sign {
		return nil
	}
	var protected []pgp.HeaderField
	for _, f := range msg.Header {
		for _, n := range pgp.ProtectedHeaderNames {
			if strings.EqualFold(f.Name, n) {
				protected = append(protected, pgp.HeaderField{Name: f.Name, Value: f.Value})
			}
		}
	}
	now := s.cfg.Now()
	var ent *pgp.MIMEEntity
	var err error
	if plan.encrypt {
		opts := pgp.EncryptOptions{ProtectedHeaders: protected, HideSubject: o.HideSubject, Now: now}
		if plan.sign {
			opts.Signer = plan.ownKey
		}
		if plan.ownKey != nil {
			opts.EncryptToSelf, opts.Self = true, plan.ownKey.Public()
		}
		ent, err = pgp.EncryptMIME(msg.Body, plan.recipients, opts)
	} else {
		ent, err = pgp.SignMIME(msg.Body, plan.ownKey, pgp.SignOptions{ProtectedHeaders: protected, Now: now})
	}
	if err != nil {
		return fmt.Errorf("send: OpenPGP: %w", err)
	}
	msg.Body = ent.Bytes()
	if ent.OuterSubject != "" {
		msg.Set("Subject", ent.OuterSubject)
	}
	res.Encrypted, res.Signed = plan.encrypt, plan.sign
	return nil
}
