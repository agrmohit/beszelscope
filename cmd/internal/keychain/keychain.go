// Package keychain reads generic passwords from the macOS Keychain.
package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// exitNotFound is the exit code of `security` when the item does not exist.
const exitNotFound = 44

// Read returns the password for service and account, or "" if there is none.
func Read(ctx context.Context, service, account string) (string, error) {
	//nolint:gosec // G204: fixed binary path; args are passed directly, never through a shell
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", service, "-a", account, "-w")
	cmd.Env = []string{}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == exitNotFound {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("security find-generic-password: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}
