// Package keychain stores beszelscope's tokens in the macOS login Keychain
// through /usr/bin/security. Items are generic passwords with service
// [Service] and the hub host as the account. Any program running as the user
// can read them through `security` without a prompt; that trade is deliberate.
package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Service is the Keychain service name of every beszelscope item.
const Service = "beszelscope"

// ErrNotFound means there is no item for the account.
var ErrNotFound = errors.New("no token in the Keychain")

// Exit codes of /usr/bin/security.
const (
	exitNotFound  = 44
	exitDuplicate = 45
)

var (
	// Commands reach `security -i` as one line split on spaces, so anything
	// else in a value could smuggle in a second command.
	validToken   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	validAccount = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)
)

// Result is what one run of /usr/bin/security produced.
type Result struct {
	Stdout string
	Stderr string
	Code   int // exit code
}

// Runner runs /usr/bin/security with args and stdin. It returns an error only
// when the process did not run to exit; a non-zero exit is a Result.
type Runner func(ctx context.Context, stdin string, args ...string) (Result, error)

// Keychain reads and writes beszelscope items.
type Keychain struct {
	run     Runner
	timeout time.Duration
}

// New returns a Keychain that runs commands with run, usually [Security].
func New(run Runner) *Keychain {
	// Reads without a prompt take milliseconds; a hang means a locked keychain.
	return &Keychain{run: run, timeout: 5 * time.Second}
}

// Get returns the token for account.
func (k *Keychain) Get(ctx context.Context, account string) (string, error) {
	if err := checkAccount(account); err != nil {
		return "", err
	}
	r, err := k.call(ctx, "", "find-generic-password", "-s", Service, "-a", account, "-w")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(r.Stdout, "\n"), nil
}

// Set replaces any token for account with token. It deletes and re-adds the
// item rather than updating it, so an old item's access rules do not survive.
func (k *Keychain) Set(ctx context.Context, account, token string) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	if !validToken.MatchString(token) {
		return errors.New("token may hold only letters, digits, '.', '-' and '_'")
	}
	err := k.Delete(ctx, account)
	replaced := err == nil
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	// The token goes in on stdin so it never shows in the process list.
	line := fmt.Sprintf("add-generic-password -s %s -a %s -w %s\n", Service, account, token)
	if _, err := k.call(ctx, line, "-i"); err != nil {
		if replaced {
			return fmt.Errorf("the old token is already deleted: %w", err)
		}
		return err
	}
	return nil
}

// Delete removes the token for account.
func (k *Keychain) Delete(ctx context.Context, account string) error {
	if err := checkAccount(account); err != nil {
		return err
	}
	_, err := k.call(ctx, "", "delete-generic-password", "-s", Service, "-a", account)
	return err
}

// List returns the accounts that have a beszelscope item, sorted. It reads
// item attributes only, never a token.
func (k *Keychain) List(ctx context.Context) ([]string, error) {
	r, err := k.call(ctx, "", "dump-keychain")
	if err != nil {
		return nil, err
	}
	return parseDump(r.Stdout), nil
}

func (k *Keychain) call(ctx context.Context, stdin string, args ...string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	r, err := k.run(ctx, stdin, args...)
	switch {
	case ctx.Err() != nil:
		return Result{}, fmt.Errorf("security %s: no answer within %s; is the Keychain locked?", args[0], k.timeout)
	case err != nil:
		return Result{}, fmt.Errorf("security %s: %w", args[0], err)
	case r.Code == exitNotFound:
		return Result{}, ErrNotFound
	case r.Code == exitDuplicate:
		return Result{}, fmt.Errorf("security %s: item already exists", args[0])
	case r.Code != 0:
		return Result{}, fmt.Errorf("security %s: exit %d: %s", args[0], r.Code, strings.TrimSpace(r.Stderr))
	}
	return r, nil
}

func checkAccount(account string) error {
	if !validAccount.MatchString(account) {
		return fmt.Errorf("account %q: want a hub host name", account)
	}
	return nil
}

// parseDump picks the account of every generic password with our service out
// of `security dump-keychain` output, where each item starts with "keychain:".
func parseDump(out string) []string {
	const svce, acct = `    "svce"<blob>="`, `    "acct"<blob>="`
	var accounts []string
	for item := range strings.SplitSeq(out, "keychain: ") {
		var service, account string
		for line := range strings.SplitSeq(item, "\n") {
			if v, ok := strings.CutPrefix(line, svce); ok {
				service = strings.TrimSuffix(v, `"`)
			}
			if v, ok := strings.CutPrefix(line, acct); ok {
				account = strings.TrimSuffix(v, `"`)
			}
		}
		if strings.Contains(item, "\nclass: \"genp\"\n") && service == Service && account != "" {
			accounts = append(accounts, account)
		}
	}
	slices.Sort(accounts)
	return slices.Compact(accounts)
}

// Security runs /usr/bin/security with an empty environment.
func Security(ctx context.Context, stdin string, args ...string) (Result, error) {
	//nolint:gosec // G204: fixed binary path; args are passed directly, never through a shell
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	cmd.Env = []string{}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.Exited() {
		return Result{Stdout: stdout.String(), Stderr: stderr.String(), Code: exitErr.ExitCode()}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("run: %w", err)
	}
	return Result{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}
