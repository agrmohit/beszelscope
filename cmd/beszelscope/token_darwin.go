package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/agrmohit/beszelscope/cmd/internal/keychain"
	"github.com/agrmohit/beszelscope/internal/config"
)

// platformUsage is the token line of the top-level help.
const platformUsage = "  beszelscope token ...      manage the hub token in the Keychain; see token --help\n"

const keychainHelp = `Usage:
  beszelscope token set      store the hub token in the Keychain, from a pipe or a prompt
  beszelscope token list     list the hubs that have a stored token
  beszelscope token delete   delete the hub token from the Keychain

set and delete act on the hub in BESZELSCOPE_URL. No command prints a token.
To check the token with the hub, run beszelscope check.
`

// systemKeychain is the macOS Keychain, through /usr/bin/security.
func systemKeychain() tokenStore {
	return keychainStore{keychain.New(keychain.Security)}
}

// keychainStore adapts the Keychain to tokenStore.
type keychainStore struct{ *keychain.Keychain }

func (k keychainStore) Get(ctx context.Context, account string) (string, error) {
	tok, err := k.Keychain.Get(ctx, account)
	if errors.Is(err, keychain.ErrNotFound) {
		return "", nil
	}
	return tok, err //nolint:wrapcheck // storedToken adds the context
}

func (keychainStore) Where(account string) string {
	return "Keychain (service " + keychain.Service + ", account " + account + ")"
}

// tokenCmd runs `beszelscope token ...`. Help goes to stdout; errors are
// returned for main to print.
func tokenCmd(ctx context.Context, args []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer, tokens tokenStore) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		_, _ = io.WriteString(stdout, keychainHelp)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "set", "list", "delete":
	case "status":
		return errors.New("token status is now beszelscope check")
	default:
		return fmt.Errorf("unknown token command %q; see beszelscope token --help", cmd)
	}
	fs := flag.NewFlagSet("token "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(rest); errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, keychainHelp)
		return nil
	} else if err != nil {
		return fmt.Errorf("token %s: %w", cmd, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("token %s takes no arguments, got %q", cmd, fs.Args())
	}

	switch cmd {
	case "set":
		return tokenSet(ctx, env, stdin, stdout, stderr, tokens, isTerminal(stdin))
	case "list":
		return tokenList(ctx, stdout, tokens)
	default:
		return tokenDelete(ctx, env, stdout, tokens)
	}
}

func tokenSet(ctx context.Context, env map[string]string, stdin io.Reader, stdout, stderr io.Writer, tokens tokenStore, prompt bool) error {
	tok, err := readToken(stdin, stderr, prompt)
	if err != nil {
		return err
	}
	// Check the new token alone: an env token here is a separate problem.
	alone := maps.Clone(env)
	delete(alone, "BESZELSCOPE_TOKEN")
	cfg, err := config.Parse(alone, tok)
	if err != nil {
		return fmt.Errorf("not stored: %w", err)
	}
	now := time.Now()
	if !cfg.TokenExpiresAt.After(now) {
		return fmt.Errorf("not stored: the token expired %s", cfg.TokenExpiresAt.Format(time.RFC3339))
	}

	// A failed check only warns: the hub may be down while the token is fine.
	if info, err := newHubClient(cfg, noop.NewTracerProvider(), quiet).Info(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "warning: the hub check failed, storing anyway: %v\n", err)
	} else {
		_, _ = fmt.Fprintf(stdout, "hub accepted the token (Beszel %s)\n", info.Version)
	}

	host := cfg.HubURL.Hostname()
	if err := tokens.Set(ctx, host, tok); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "stored the token for %s (user %s, %s)\n", host, cfg.TokenUserID, expiry(now, cfg.TokenExpiresAt))
	if env["BESZELSCOPE_TOKEN"] != "" {
		_, _ = io.WriteString(stderr, "warning: BESZELSCOPE_TOKEN is also set; unset it, or the server refuses to start\n")
	}
	return nil
}

// readToken reads one token. On a terminal it prompts and reads one line;
// otherwise it reads all of stdin.
func readToken(stdin io.Reader, stderr io.Writer, prompt bool) (string, error) {
	var raw string
	if prompt {
		_, _ = io.WriteString(stderr, "Paste the token and press Enter (it shows on screen): ")
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read token: %w", err)
		}
		raw = line
	} else {
		b, err := io.ReadAll(io.LimitReader(stdin, 64<<10))
		if err != nil {
			return "", fmt.Errorf("read token: %w", err)
		}
		raw = string(b)
	}
	tok := strings.TrimSpace(raw)
	if tok == "" {
		return "", errors.New("no token on stdin; try `pbpaste | beszelscope token set`")
	}
	return tok, nil
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func tokenList(ctx context.Context, stdout io.Writer, tokens tokenStore) error {
	hosts, err := tokens.List(ctx)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if len(hosts) == 0 {
		_, _ = io.WriteString(stdout, "no tokens stored\n")
		return nil
	}
	now := time.Now()
	for _, host := range hosts {
		line := "unreadable: "
		tok, err := tokens.Get(ctx, host)
		if err == nil {
			var info config.TokenInfo
			if info, err = config.ParseToken(tok); err == nil {
				line = expiry(now, info.ExpiresAt)
			}
		}
		if err != nil {
			line += err.Error()
		}
		_, _ = fmt.Fprintf(stdout, "%s  %s\n", host, line)
	}
	return nil
}

func tokenDelete(ctx context.Context, env map[string]string, stdout io.Writer, tokens tokenStore) error {
	u, err := url.Parse(env["BESZELSCOPE_URL"])
	if err != nil || u.Hostname() == "" {
		return errors.New("set BESZELSCOPE_URL to the hub whose token to delete")
	}
	host := u.Hostname()
	if err := tokens.Delete(ctx, host); errors.Is(err, keychain.ErrNotFound) {
		return fmt.Errorf("no token stored for %s", host)
	} else if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "deleted the token for %s\n", host)
	return nil
}
