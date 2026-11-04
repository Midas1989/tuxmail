// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import "github.com/Midas1989/Mailclient/internal/send"

// OutgoingCrypto is the composer's choice of signing/encryption.
type OutgoingCrypto struct {
	// Method: "auto" (settings: preferred method when possible), "smime",
	// "pgp" or "none".
	Method  string `json:"method,omitempty"`
	Sign    bool   `json:"sign,omitempty"`
	Encrypt bool   `json:"encrypt,omitempty"`
}

func onOff(b bool) send.PGPMode {
	if b {
		return send.PGPOn
	}
	return send.PGPOff
}

// applyOutgoingCrypto maps the composer choice onto the send request.
func (a *App) applyOutgoingCrypto(req *send.Request, c *OutgoingCrypto) {
	s := a.comfortSettings().SMIME
	method := "auto"
	if c != nil && c.Method != "" {
		method = c.Method
	}
	switch method {
	case "none":
		req.PGP.Sign, req.PGP.Encrypt = send.PGPOff, send.PGPOff
		req.SMIME = send.SMIMEOptions{Sign: send.PGPOff, Encrypt: send.PGPOff}
	case "smime":
		req.PGP.Sign, req.PGP.Encrypt = send.PGPOff, send.PGPOff
		req.SMIME = send.SMIMEOptions{Sign: onOff(c.Sign), Encrypt: onOff(c.Encrypt)}
	case "pgp":
		req.PGP.Sign, req.PGP.Encrypt = onOff(c.Sign), onOff(c.Encrypt)
		req.SMIME = send.SMIMEOptions{Sign: send.PGPOff, Encrypt: send.PGPOff}
	default:
		// Automatic: OpenPGP keeps its Autocrypt logic; S/MIME encrypts when
		// every recipient has a certificate (if enabled), signs by setting.
		req.SMIME = send.SMIMEOptions{Sign: send.PGPAuto, Encrypt: send.PGPOff}
		if s.EncryptAutomatically {
			req.SMIME.Encrypt = send.PGPAuto
		}
		if s.Prefer == "smime" {
			// Prefer S/MIME: sign whenever a certificate exists.
			req.SMIME.SignAlways = s.SignByDefault
		} else if s.SignByDefault {
			req.SMIME.SignAlways = true
		}
	}
}
