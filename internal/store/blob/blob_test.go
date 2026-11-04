// SPDX-License-Identifier: AGPL-3.0-or-later

package blob

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "blobs")
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func readAll(t *testing.T, s *Store, h string) []byte {
	t.Helper()
	rc, err := s.Open(h)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPutOpenRoundTrip(t *testing.T) {
	s, root := newStore(t)
	big := make([]byte, 3<<20)
	rand.Read(big[:1<<20])
	for _, data := range [][]byte{nil, []byte("hello world"), bytes.Repeat([]byte("abc"), 100000), big} {
		h, err := s.Put(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if h != hex.EncodeToString(sum[:]) {
			t.Fatalf("hash mismatch")
		}
		if _, err := os.Stat(filepath.Join(root, h[:2], h[2:4], h)); err != nil {
			t.Fatalf("expected layout ab/cd/<hash>: %v", err)
		}
		if got := readAll(t, s, h); !bytes.Equal(got, data) {
			t.Fatalf("content mismatch for %d bytes", len(data))
		}
		if ok, err := s.Has(h); err != nil || !ok {
			t.Fatalf("Has = %v, %v", ok, err)
		}
	}
}

func TestCompressionAndDedup(t *testing.T) {
	s, root := newStore(t)
	data := bytes.Repeat([]byte("Subject: compress me\r\n"), 10000)
	h1, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, h1[:2], h1[2:4], h1))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() >= int64(len(data))/10 {
		t.Fatalf("not compressed: %d of %d bytes", fi.Size(), len(data))
	}
	h2, err := s.Put(bytes.NewReader(data))
	if err != nil || h2 != h1 {
		t.Fatalf("dedup: %v %v", h2, err)
	}
	tmp, _ := os.ReadDir(filepath.Join(root, tmpDir))
	if len(tmp) != 0 {
		t.Fatalf("temporary files left: %v", tmp)
	}
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, errors.New("boom")
	}
	n := min(len(p), r.n)
	r.n -= n
	return n, nil
}

func TestPutFailureLeavesNothing(t *testing.T) {
	s, root := newStore(t)
	if _, err := s.Put(&failingReader{n: 5000}); err == nil {
		t.Fatal("expected error")
	}
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Fatalf("files left after failed put: %v", files)
	}
}

func TestMissingInvalidDelete(t *testing.T) {
	s, _ := newStore(t)
	missing := strings.Repeat("0", 64)
	if _, err := s.Open(missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if ok, err := s.Has(missing); ok || err != nil {
		t.Fatalf("Has missing = %v, %v", ok, err)
	}
	for _, bad := range []string{"", "../../etc/passwd", strings.Repeat("A", 64), strings.Repeat("0", 63)} {
		if _, err := s.Open(bad); !errors.Is(err, ErrInvalidHash) {
			t.Fatalf("Open(%q): want ErrInvalidHash, got %v", bad, err)
		}
		if err := s.Delete(bad); !errors.Is(err, ErrInvalidHash) {
			t.Fatalf("Delete(%q): want ErrInvalidHash, got %v", bad, err)
		}
	}
	h, err := s.Put(strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(h); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Has(h); ok {
		t.Fatal("still present after delete")
	}
	if err := s.Delete(h); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestWalkAndConcurrentPut(t *testing.T) {
	s, _ := newStore(t)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		want = map[string]bool{}
	)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Pairs of goroutines write identical content.
			h, err := s.Put(strings.NewReader(strings.Repeat("x", i/2+1)))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			want[h] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	var got []string
	if err := s.Walk(func(h string) error { got = append(got, h); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 || len(want) != 8 {
		t.Fatalf("walk found %d blobs, want 8 (%d unique puts)", len(got), len(want))
	}
	sort.Strings(got)
	for _, h := range got {
		if !want[h] {
			t.Fatalf("unexpected blob %s", h)
		}
	}
	stop := errors.New("stop")
	if err := s.Walk(func(string) error { return stop }); !errors.Is(err, stop) {
		t.Fatalf("walk error not propagated: %v", err)
	}
}

// Close is called by net/http's transport and by the caller's defer, possibly
// at the same time (data race seen in CI).
func TestReaderConcurrentClose(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Put(strings.NewReader("hello blob"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		rc, err := s.Open(h)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func() { defer wg.Done(); rc.Close() }()
		}
		wg.Wait()
	}
}
