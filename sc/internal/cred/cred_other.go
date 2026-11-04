//go:build !windows

package cred

import "errors"

type unsupported struct{}

// New returns a store that refuses, so a non-Windows build still compiles.
func New(string) Store { return unsupported{} }

var errUnsupported = errors.New("storing SQL passwords is only supported on Windows in this version")

func (unsupported) Set(int64, string) error   { return errUnsupported }
func (unsupported) Get(int64) (string, error) { return "", errUnsupported }
func (unsupported) Delete(int64) error        { return nil }
