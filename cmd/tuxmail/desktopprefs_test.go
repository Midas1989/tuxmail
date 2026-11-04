// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestDesktopPrefsTitleBar(t *testing.T) {
	dir := t.TempDir()
	p := loadDesktopPrefs(dir)
	if !p.nativeTitleBar("linux") || p.nativeTitleBar("windows") {
		t.Fatal("defaults: native on Linux, custom on Windows")
	}
	d := &Desktop{dataDir: dir}
	if err := d.SetNativeTitleBar(true); err != nil {
		t.Fatal(err)
	}
	if !loadDesktopPrefs(dir).nativeTitleBar("windows") {
		t.Error("setting not saved")
	}
	if err := d.SetNativeTitleBar(false); err != nil {
		t.Fatal(err)
	}
	if loadDesktopPrefs(dir).nativeTitleBar("linux") || d.WindowInfo().NativeTitleBar {
		t.Error("setting not applied")
	}
}
