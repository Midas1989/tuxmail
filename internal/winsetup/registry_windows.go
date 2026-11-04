// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package winsetup

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// WinRegistry is the real registry (64-bit view).
type WinRegistry struct{}

var _ Registry = WinRegistry{}

func hive(r Root) registry.Key {
	if r == LocalMachine {
		return registry.LOCAL_MACHINE
	}
	return registry.CURRENT_USER
}

func mapErr(err error) error {
	if errors.Is(err, registry.ErrNotExist) {
		return ErrNotExist
	}
	return err
}

func (WinRegistry) GetString(r Root, path, name string) (string, error) {
	k, err := registry.OpenKey(hive(r), path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", mapErr(err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	return v, mapErr(err)
}

func (WinRegistry) GetDWORD(r Root, path, name string) (uint32, error) {
	k, err := registry.OpenKey(hive(r), path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return 0, mapErr(err)
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return uint32(v), mapErr(err)
}

func (WinRegistry) SetString(r Root, path, name, value string) error {
	k, _, err := registry.CreateKey(hive(r), path, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(name, value)
}

func (WinRegistry) SetDWORD(r Root, path, name string, value uint32) error {
	k, _, err := registry.CreateKey(hive(r), path, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetDWordValue(name, value)
}

func (WinRegistry) DeleteValue(r Root, path, name string) error {
	k, err := registry.OpenKey(hive(r), path, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return mapErr(err)
	}
	defer k.Close()
	return mapErr(k.DeleteValue(name))
}

func (w WinRegistry) DeleteTree(r Root, path string) error {
	k, err := registry.OpenKey(hive(r), path, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return mapErr(err)
	}
	names, err := k.ReadSubKeyNames(-1)
	k.Close()
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := w.DeleteTree(r, path+`\`+n); err != nil && !errors.Is(err, ErrNotExist) {
			return err
		}
	}
	return mapErr(registry.DeleteKey(hive(r), path))
}
