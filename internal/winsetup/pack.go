// SPDX-License-Identifier: AGPL-3.0-or-later

package winsetup

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"
)

// ZipMethodZstd is the zip method of payload entries (Zstandard, as used
// by WinZip/7-Zip; archive/zip knows only store and deflate).
const ZipMethodZstd = zstd.ZipMethodWinZip

// payloadWindow is the zstd window: executables compress noticeably better
// with a large window; decompression needs about this much memory.
const payloadWindow = 32 << 20

func registerZstd(zr *zip.Reader) {
	zr.RegisterDecompressor(ZipMethodZstd, zstd.ZipDecompressor(zstd.WithDecoderMaxWindow(payloadWindow), zstd.WithDecoderConcurrency(1)))
}

// WritePayload packs all regular files below dir into a payload zip
// (zstd, sorted names, fixed modification time for reproducible builds).
func WritePayload(w io.Writer, dir string, mtime time.Time) error {
	var names []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			names = append(names, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(names)
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(ZipMethodZstd, zstd.ZipCompressor(
		zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithWindowSize(payloadWindow), zstd.WithEncoderConcurrency(1)))
	for _, n := range names {
		if _, err := safeName(n); err != nil {
			return err
		}
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(n)))
		if err != nil {
			return err
		}
		hdr := &zip.FileHeader{Name: n, Method: ZipMethodZstd, Modified: mtime.UTC()}
		hdr.SetMode(0o755)
		out, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(out, f)
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}
