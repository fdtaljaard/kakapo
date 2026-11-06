//go:build windows

package main

import (
	"golang.org/x/sys/windows/registry"
)

// edgeAppPaths is where Windows itself says msedge.exe is: the App Paths key's default value, the
// machine's (HKLM) then the user's (HKCU). Values that are missing or unreadable are left out.
func edgeAppPaths() []string {
	const key = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`
	out := []string{}
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, key, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		v, typ, err := k.GetStringValue("")
		if err == nil && typ == registry.EXPAND_SZ {
			v, err = registry.ExpandString(v)
		}
		if err == nil && v != "" {
			out = append(out, v)
		}
		k.Close()
	}
	return out
}
