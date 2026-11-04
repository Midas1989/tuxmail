// SPDX-License-Identifier: AGPL-3.0-or-later

package pgp

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"html"
	"io"
	"math/big"
	"mime"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

// Autocrypt Setup Message constants (Autocrypt Level 1, section 4.4).
const (
	SetupMessageHeader      = "Autocrypt-Setup-Message"
	SetupMessageSubject     = "Autocrypt Setup Message"
	SetupMessageContentType = "application/autocrypt-setup"
)

// ErrInvalidSetupCode is returned when the setup code is malformed or wrong.
var ErrInvalidSetupCode = errors.New("pgp: invalid Autocrypt setup code")

// GenerateSetupCode returns a random 36-digit setup code formatted as nine
// dash-separated blocks of four digits.
func GenerateSetupCode(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	var digits strings.Builder
	ten := big.NewInt(10)
	for i := 0; i < 36; i++ {
		n, err := rand.Int(r, ten)
		if err != nil {
			return "", err
		}
		// #nosec G115 -- n is a random digit 0..9
		digits.WriteByte(byte('0' + n.Int64()))
	}
	return formatSetupCode(digits.String()), nil
}

func formatSetupCode(digits string) string {
	var blocks []string
	for i := 0; i < len(digits); i += 4 {
		blocks = append(blocks, digits[i:min(i+4, len(digits))])
	}
	return strings.Join(blocks, "-")
}

// NormalizeSetupCode accepts a setup code with arbitrary separators and
// returns it in canonical form, or ErrInvalidSetupCode.
func NormalizeSetupCode(code string) (string, error) {
	var digits strings.Builder
	for _, r := range code {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '-' || r == ' ' || r == '\t' || r == '\r' || r == '\n':
		default:
			return "", ErrInvalidSetupCode
		}
	}
	if digits.Len() != 36 {
		return "", ErrInvalidSetupCode
	}
	return formatSetupCode(digits.String()), nil
}

// SetupMessage is a generated Autocrypt Setup Message.
type SetupMessage struct {
	// Entity is the multipart/mixed MIME entity including the
	// "Autocrypt-Setup-Message: v1" header. The composer adds From, To
	// (both the user's own address), Date and Subject (SetupMessageSubject).
	Entity *MIMEEntity
	// Attachment is the content of the application/autocrypt-setup part.
	Attachment []byte
	// SetupCode must be shown to the user (never stored or sent).
	SetupCode string
}

// CreateSetupMessage builds an Autocrypt Setup Message transferring the
// unlocked secret key to another device. prefer is written as the
// Autocrypt-Prefer-Encrypt armor header.
func CreateSetupMessage(secret *Key, prefer PreferEncrypt) (*SetupMessage, error) {
	if !secret.IsSecret() {
		return nil, ErrNotSecretKey
	}
	if secret.IsLocked() {
		return nil, ErrKeyLocked
	}
	code, err := GenerateSetupCode(nil)
	if err != nil {
		return nil, err
	}
	if prefer == "" {
		prefer = PreferNoPreference
	}
	sb, err := secret.SecretBytes()
	if err != nil {
		return nil, err
	}
	keyArmored, err := armorBytes(sb, armorPrivateKey, map[string]string{"Autocrypt-Prefer-Encrypt": string(prefer)})
	if err != nil {
		return nil, err
	}

	var enc bytes.Buffer
	aw, err := armor.EncodeWithChecksumOption(&enc, armorMessage, map[string]string{
		"Passphrase-Format": "numeric9x4",
		"Passphrase-Begin":  code[:2],
	}, false)
	if err != nil {
		return nil, err
	}
	pw, err := openpgp.SymmetricallyEncrypt(aw, []byte(code), nil, &packet.Config{DefaultCipher: packet.CipherAES128})
	if err != nil {
		return nil, fmt.Errorf("pgp: setup message: %w", err)
	}
	if _, err := io.WriteString(pw, keyArmored); err != nil {
		return nil, err
	}
	if err := pw.Close(); err != nil {
		return nil, err
	}
	if err := aw.Close(); err != nil {
		return nil, err
	}

	attachment := []byte("<html><body>\r\n" +
		"<p>This is the Autocrypt Setup File used to transfer settings and keys between clients. " +
		"You can decrypt it using the Setup Code presented on your old device, and then import the contained key into your keyring.</p>\r\n" +
		"<pre>\r\n" + html.EscapeString(string(CanonicalizeCRLF(enc.Bytes()))) + "\r\n</pre></body></html>")

	boundary := randomBoundary()
	var body bytes.Buffer
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	body.WriteString("This message contains all information to transfer your Autocrypt settings along with your secret key securely from your original device.\r\n\r\n")
	body.WriteString("To set up your new device for Autocrypt, please follow the instructions that should be presented by your new device.\r\n\r\n")
	body.WriteString("You can keep this message and use it as a backup for your secret key. If you want to do this, you should write down the Setup Code and store it securely.\r\n")
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString("Content-Type: " + SetupMessageContentType + "\r\n")
	body.WriteString("Content-Disposition: attachment; filename=\"autocrypt-setup-message.html\"\r\n\r\n")
	body.Write(attachment)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	return &SetupMessage{
		Entity: &MIMEEntity{
			Header: []HeaderField{
				{Name: SetupMessageHeader, Value: "v1"},
				{Name: "Content-Type", Value: mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": boundary})},
			},
			Body: body.Bytes(),
		},
		Attachment: attachment,
		SetupCode:  code,
	}, nil
}

// ExtractSetupAttachment finds the application/autocrypt-setup attachment
// in a complete Autocrypt Setup Message.
func ExtractSetupAttachment(msg []byte) ([]byte, error) {
	msg = CanonicalizeCRLF(msg)
	hdr, body := splitEntity(msg)
	h, err := parseHeader(hdr)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(h.Get(SetupMessageHeader)), "v1") {
		return nil, fmt.Errorf("%w: not an Autocrypt Setup Message", ErrMalformedMIME)
	}
	mt, params := mediaType(h.Get("Content-Type"))
	if !strings.HasPrefix(mt, "multipart/") {
		return nil, fmt.Errorf("%w: setup message is not multipart", ErrMalformedMIME)
	}
	parts, err := splitMultipart(body, params["boundary"])
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		ph, pb := splitEntity(p)
		phh, err := parseHeader(ph)
		if err != nil {
			continue
		}
		if pmt, _ := mediaType(phh.Get("Content-Type")); pmt == SetupMessageContentType {
			return decodeTransfer(pb, phh.Get("Content-Transfer-Encoding"))
		}
	}
	return nil, fmt.Errorf("%w: no %s part", ErrMalformedMIME, SetupMessageContentType)
}

// ImportSetupMessage decrypts the content of an application/autocrypt-setup
// attachment with the setup code and returns the contained secret key (as
// transferred, i.e. unprotected) and the prefer-encrypt setting.
func ImportSetupMessage(attachment []byte, setupCode string) (*Key, PreferEncrypt, error) {
	code, err := NormalizeSetupCode(setupCode)
	if err != nil {
		return nil, "", err
	}
	text := html.UnescapeString(string(attachment))
	i := strings.Index(text, "-----BEGIN PGP MESSAGE-----")
	if i < 0 {
		return nil, "", fmt.Errorf("%w: no PGP MESSAGE in setup file", ErrMalformedMIME)
	}
	block, err := armor.Decode(strings.NewReader(text[i:]))
	if err != nil {
		return nil, "", fmt.Errorf("pgp: setup message armor: %w", err)
	}
	tried := false
	prompt := func(keys []openpgp.Key, symmetric bool) ([]byte, error) {
		if tried || !symmetric {
			return nil, ErrInvalidSetupCode
		}
		tried = true
		return []byte(code), nil
	}
	md, err := openpgp.ReadMessage(block.Body, nil, prompt, nil)
	if err != nil {
		if tried || errors.Is(err, ErrInvalidSetupCode) {
			return nil, "", ErrInvalidSetupCode
		}
		return nil, "", fmt.Errorf("pgp: setup message: %w", err)
	}
	plain, err := io.ReadAll(md.UnverifiedBody)
	if err != nil {
		// Integrity check failed: almost certainly a wrong setup code.
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidSetupCode, err)
	}
	kb, err := armor.Decode(bytes.NewReader(plain))
	if err != nil {
		return nil, "", fmt.Errorf("pgp: setup message key armor: %w", err)
	}
	prefer := PreferNoPreference
	if strings.EqualFold(kb.Header["Autocrypt-Prefer-Encrypt"], string(PreferMutual)) {
		prefer = PreferMutual
	}
	keys, err := ReadKeys(plain)
	if err != nil {
		return nil, "", err
	}
	if !keys[0].IsSecret() {
		return nil, "", ErrNotSecretKey
	}
	return keys[0], prefer, nil
}
