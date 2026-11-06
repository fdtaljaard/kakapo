//go:build !windows

package main

// edgeAppPaths has nothing to read outside Windows.
func edgeAppPaths() []string { return nil }
