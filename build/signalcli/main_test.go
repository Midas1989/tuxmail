// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"testing"
)

func jarWithMain(t *testing.T, major byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, err := zw.Create("org/asamk/signal/Main.class")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte{0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, major, 1, 2, 3})
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRequiredJava(t *testing.T) {
	jar := jarWithMain(t, 69)
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, data := range map[string][]byte{
		"signal-cli-0.14.9/lib/other.jar":             jarWithMain(t, 52),
		"signal-cli-0.14.9/lib/signal-cli-0.14.9.jar": jar,
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gz.Close()

	got, err := requiredJava(b.Bytes(), "0.14.9")
	if err != nil || got != 25 {
		t.Fatalf("requiredJava = %d, %v; want 25", got, err)
	}
	if _, err := requiredJava(b.Bytes(), "0.15.0"); err == nil {
		t.Fatal("missing jar accepted")
	}
	if j, err := classJava(jarWithMain(t, 65), "org/asamk/signal/Main.class"); err != nil || j != 21 {
		t.Fatalf("class file 65: %d %v", j, err)
	}
	if _, err := classJava(jarWithMain(t, 40), "org/asamk/signal/Main.class"); err == nil {
		t.Fatal("bogus class file version accepted")
	}
}
