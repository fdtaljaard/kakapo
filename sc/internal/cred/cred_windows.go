//go:build windows

package cred

import (
	"errors"

	"github.com/danieljoos/wincred"
)

// WindowsCredentialManager stores passwords in the current user's Credential Manager,
// which is what the PowerShell runners and Darling Lite do too.
type WindowsCredentialManager struct {
	installID string
}

// New returns the platform credential store for this install.
func New(installID string) Store { return WindowsCredentialManager{installID: installID} }

func (w WindowsCredentialManager) Set(serverID int64, password string) error {
	c := wincred.NewGenericCredential(target(w.installID, serverID))
	c.CredentialBlob = []byte(password)
	c.Persist = wincred.PersistLocalMachine
	c.UserName = "SQLMonitor"
	return c.Write()
}

func (w WindowsCredentialManager) Get(serverID int64) (string, error) {
	c, err := wincred.GetGenericCredential(target(w.installID, serverID))
	if err != nil {
		return "", errors.New("no stored password for this server")
	}
	return string(c.CredentialBlob), nil
}

func (w WindowsCredentialManager) Delete(serverID int64) error {
	c, err := wincred.GetGenericCredential(target(w.installID, serverID))
	if err != nil {
		return nil
	}
	return c.Delete()
}
