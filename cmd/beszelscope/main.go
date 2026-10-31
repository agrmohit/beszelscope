// Command beszelscope is a read-only MCP server for a Beszel hub. With no
// arguments it serves MCP over stdin and stdout.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/agrmohit/beszelscope/cmd/internal/keychain"
)

var version = "dev" // set by make build

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr, systemKeychain())
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "beszelscope:", err)
		os.Exit(1)
	}
}

// tokenStore holds hub tokens by hub host. *keychain.Keychain is the real one.
type tokenStore interface {
	Get(ctx context.Context, account string) (string, error)
	Set(ctx context.Context, account, token string) error
	Delete(ctx context.Context, account string) error
	List(ctx context.Context) ([]string, error)
}

// systemKeychain returns the macOS Keychain, or nil elsewhere: other systems
// take the token from BESZELSCOPE_TOKEN only.
func systemKeychain() tokenStore {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return keychain.New(keychain.Security)
}

// run is the whole program. environ is in os.Environ form; tokens is nil
// where there is no Keychain.
func run(ctx context.Context, args, environ []string, stdin io.Reader, stdout, stderr io.Writer, tokens tokenStore) error {
	if len(args) == 0 {
		return serve(ctx, envMap(environ), stdin, stdout, stderr, tokens)
	}
	return fmt.Errorf("unknown command %q; run beszelscope with no arguments to serve MCP over stdio", args[0])
}

func envMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
