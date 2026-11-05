// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package winsetup

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modCrypt32         = windows.NewLazySystemDLL("crypt32.dll")
	procCryptMsgGetPar = modCrypt32.NewProc("CryptMsgGetParam")
	procCryptMsgClose  = modCrypt32.NewProc("CryptMsgClose")
)

// cmsgSignerCertInfoParam is CMSG_SIGNER_CERT_INFO_PARAM: the CERT_INFO
// (issuer and serial number) of the signer certificate of a signed message.
const cmsgSignerCertInfoParam = 7

// VerifyAuthenticode checks the embedded Authenticode signature of path with
// WinVerifyTrust (chain to a trusted root, file hash matches, no revocation
// lookup so that offline machines can install) and that the signer
// certificate's display name is accepted by publisher (TrustedPublisher for
// Microsoft's bootstrapper). The bootstrapper is run elevated by many
// installations, so an unsigned or foreign-signed file must never be
// executed (re-audit ND-04).
func VerifyAuthenticode(path string, publisher func(name string) bool) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	data := &windows.WinTrustData{
		Size:             uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:         windows.WTD_UI_NONE,
		RevocationChecks: windows.WTD_REVOKE_NONE,
		UnionChoice:      windows.WTD_CHOICE_FILE,
		StateAction:      windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&windows.WinTrustFileInfo{
			Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
			FilePath: p,
		}),
	}
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	_ = windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	if verifyErr != nil {
		return fmt.Errorf("%s: Authenticode signature is not valid: %w", path, verifyErr)
	}
	name, err := signerName(p)
	if err != nil {
		return fmt.Errorf("%s: signer: %w", path, err)
	}
	if !publisher(name) {
		return fmt.Errorf("%s is signed by %q", path, name)
	}
	return nil
}

// signerName returns the display name of the signer certificate of the
// embedded signature of the file.
func signerName(path *uint16) (string, error) {
	var store, msg windows.Handle
	err := windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(path),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY, 0,
		nil, nil, nil, &store, &msg, nil)
	if err != nil {
		return "", err
	}
	defer windows.CertCloseStore(store, 0)
	defer procCryptMsgClose.Call(uintptr(msg))

	var n uint32
	if r, _, e := procCryptMsgGetPar.Call(uintptr(msg), cmsgSignerCertInfoParam, 0, 0, uintptr(unsafe.Pointer(&n))); r == 0 {
		return "", e
	}
	if n == 0 {
		return "", errors.New("no signer information")
	}
	buf := make([]byte, n)
	if r, _, e := procCryptMsgGetPar.Call(uintptr(msg), cmsgSignerCertInfoParam, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n))); r == 0 {
		return "", e
	}
	info := (*windows.CertInfo)(unsafe.Pointer(&buf[0]))
	cert, err := windows.CertFindCertificateInStore(store, windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, 0,
		windows.CERT_FIND_SUBJECT_CERT, unsafe.Pointer(info), nil)
	if err != nil {
		return "", err
	}
	defer windows.CertFreeCertificateContext(cert)

	size := windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, nil, 0)
	if size <= 1 {
		return "", errors.New("signer certificate has no name")
	}
	name := make([]uint16, size)
	windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, &name[0], size)
	return windows.UTF16ToString(name), nil
}
