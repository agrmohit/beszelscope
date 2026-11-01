//go:build !darwin

package main

import (
	"context"
	"io"
)

// platformUsage is empty: there is no Keychain, so no token commands.
const platformUsage = ""

// systemKeychain is nil off macOS: the token comes from BESZELSCOPE_TOKEN.
func systemKeychain() tokenStore { return nil }

// tokenCmd answers as for any unknown command: the token commands manage the
// macOS Keychain and are built only for macOS.
func tokenCmd(_ context.Context, _ []string, _ map[string]string, _ io.Reader, _, _ io.Writer, _ tokenStore) error {
	return errUnknownCommand("token")
}
