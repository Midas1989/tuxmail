// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"strconv"
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"net/http"
	"strings"

	"github.com/Midas1989/Mailclient/internal/domain"
	"github.com/Midas1989/Mailclient/internal/mime/signature"
)

// Signatures are full HTML (KONZEPT 8.2): the UI edits them with data:
// URIs for images; on save the HTML is sanitized, data: images are stored
// in the blob store and referenced as cid:, and the plain-text version is
// generated unless the user wrote one (PlainOverride). The UI receives the
// images back as data: URIs, so it can show and insert them without a
// separate image endpoint; composing turns them into multipart/related
// inline parts again.

// ListSignatures returns all signatures (images as data: URIs).
func (a *App) ListSignatures(ctx context.Context) ([]Signature, error) {
	sigs, err := a.st.ListSignatures(ctx)
	if sigs == nil {
		sigs = []Signature{}
	}
	for i := range sigs {
		sigs[i] = a.signatureForUI(sigs[i])
	}
	return sigs, err
}

// SaveSignature creates (ID 0) or updates a signature. A missing HTML
// version is generated from the plain text, a missing (or not overridden)
// plain text from the HTML.
func (a *App) SaveSignature(ctx context.Context, sig Signature) (Signature, error) {
	sig.Name = strings.TrimSpace(sig.Name)
	if sig.Name == "" {
		return Signature{}, invalid("signature name required")
	}
	switch sig.Placement {
	case "", "below_reply", "above_quote":
	default:
		return Signature{}, invalid("invalid placement %q", sig.Placement)
	}
	var old []string
	if sig.ID != 0 {
		prev, err := a.st.GetSignature(ctx, sig.ID)
		if err != nil {
			return Signature{}, err
		}
		for _, im := range prev.Images {
			old = append(old, im.BlobHash)
		}
	}
	sig, err := a.prepareSignature(ctx, sig)
	if err != nil {
		return Signature{}, err
	}
	if sig.ID == 0 {
		id, err := a.st.CreateSignature(ctx, sig)
		if err != nil {
			return Signature{}, err
		}
		sig.ID = id
	} else if err := a.st.UpdateSignature(ctx, sig); err != nil {
		return Signature{}, err
	}
	a.dropUnreferencedBlobs(ctx, old)
	a.hub.Publish(Event{Type: EventAccountUpdated})
	got, err := a.st.GetSignature(ctx, sig.ID)
	return a.signatureForUI(got), err
}

// DeleteSignature deletes a signature; identities using it fall back to none.
func (a *App) DeleteSignature(ctx context.Context, id int64) error {
	var hashes []string
	if prev, err := a.st.GetSignature(ctx, id); err == nil {
		for _, im := range prev.Images {
			hashes = append(hashes, im.BlobHash)
		}
	}
	if err := a.st.DeleteSignature(ctx, id); err != nil {
		return err
	}
	a.noteDeleted(ctx, settingsKindSignature, strconv.FormatInt(id, 10))
	a.dropUnreferencedBlobs(ctx, hashes)
	a.hub.Publish(Event{Type: EventAccountUpdated})
	return nil
}

// createImportedSignature stores a signature from an import or a
// provisioning profile through the same pipeline as SaveSignature.
func (a *App) createImportedSignature(ctx context.Context, sig Signature) (int64, error) {
	sig.ID = 0
	if strings.TrimSpace(sig.Name) == "" {
		sig.Name = "Signature"
	}
	if strings.TrimSpace(sig.Plain) != "" && strings.TrimSpace(sig.HTML) != "" {
		sig.PlainOverride = true // keep the plain version the source had
	}
	sig, err := a.prepareSignature(ctx, sig)
	if err != nil {
		return 0, err
	}
	return a.st.CreateSignature(ctx, sig)
}

// prepareSignature sanitizes the HTML, stores data: images, resolves cid:
// references and generates the plain-text version.
func (a *App) prepareSignature(ctx context.Context, sig Signature) (Signature, error) {
	if strings.TrimSpace(sig.HTML) == "" && sig.Plain != "" {
		sig.HTML = strings.ReplaceAll(html.EscapeString(strings.TrimRight(sig.Plain, "\n")), "\n", "<br>")
	}
	clean := signature.Sanitize(sig.HTML)
	clean, imgs, err := signature.ExtractDataImages(clean)
	switch {
	case errors.Is(err, signature.ErrImageTooLarge):
		return sig, Errorf(http.StatusRequestEntityTooLarge, "too_large", "a signature image is larger than %d KB", signature.MaxImageSize>>10)
	case errors.Is(err, signature.ErrBadImage):
		return sig, invalid("unsupported signature image: PNG, JPEG, GIF or WebP expected")
	case err != nil:
		return sig, err
	}
	known := map[string]domain.SignatureImage{}
	if sig.ID != 0 {
		if prev, err := a.st.GetSignature(ctx, sig.ID); err == nil {
			for _, im := range prev.Images {
				known[im.ContentID] = im
			}
		}
	}
	for _, img := range imgs {
		if a.blobs == nil {
			return sig, notSupported("signature images")
		}
		h, err := a.blobs.Put(bytes.NewReader(img.Data))
		if err != nil {
			return sig, err
		}
		known[img.ContentID] = domain.SignatureImage{ContentID: img.ContentID, MIMEType: img.MIMEType, Filename: img.Filename, BlobHash: h, Size: int64(len(img.Data))}
	}
	var used []domain.SignatureImage
	seen := map[string]bool{}
	clean = signature.RewriteImages(clean, func(src string) (string, bool) {
		if !strings.HasPrefix(strings.ToLower(src), "cid:") {
			return src, true // remote https image (the sanitizer removed anything else)
		}
		cid := strings.Trim(src[4:], "<> ")
		im, ok := known[cid]
		if !ok {
			// An image of another signature (copied HTML).
			var err error
			if im, err = a.st.SignatureImageByCID(ctx, cid); err != nil {
				return "", false
			}
		}
		if !seen[cid] {
			seen[cid] = true
			used = append(used, im)
		}
		return "cid:" + cid, true
	})
	if len(clean) > signature.MaxHTMLSize {
		return sig, invalid("signature is too long")
	}
	sig.HTML, sig.Images = clean, used
	if !sig.PlainOverride || strings.TrimSpace(sig.Plain) == "" {
		sig.Plain, sig.PlainOverride = signature.ToPlain(clean), false
	}
	sig.Plain = strings.TrimRight(strings.ReplaceAll(sig.Plain, "\r\n", "\n"), "\n ")
	return sig, nil
}

// signatureForUI replaces cid: references by data: URIs.
func (a *App) signatureForUI(sig Signature) Signature {
	if len(sig.Images) == 0 || a.blobs == nil {
		return sig
	}
	byCID := map[string]domain.SignatureImage{}
	for _, im := range sig.Images {
		byCID[im.ContentID] = im
	}
	sig.HTML = signature.RewriteImages(sig.HTML, func(src string) (string, bool) {
		if !strings.HasPrefix(strings.ToLower(src), "cid:") {
			return src, true
		}
		im, ok := byCID[strings.Trim(src[4:], "<> ")]
		if !ok {
			return src, true
		}
		rc, err := a.blobs.Open(im.BlobHash)
		if err != nil {
			return src, true
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, signature.MaxImageSize+1))
		if err != nil {
			return src, true
		}
		return signature.DataURI(im.MIMEType, data), true
	})
	return sig
}

// dropUnreferencedBlobs deletes blobs no row references any more.
func (a *App) dropUnreferencedBlobs(ctx context.Context, hashes []string) {
	if a.blobs == nil || len(hashes) == 0 {
		return
	}
	un, err := a.st.UnreferencedBlobs(ctx, hashes)
	if err != nil {
		return
	}
	for _, h := range un {
		_ = a.blobs.Delete(h)
	}
}
