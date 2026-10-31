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
	"runtime/debug"
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
	switch args[0] {
	case "token":
		return tokenCmd(ctx, args[1:], envMap(environ), stdin, stdout, stderr, tokens)
	case "version":
		_, _ = io.WriteString(stdout, versionLine()+"\n")
		return nil
	case "-h", "--help", "help":
		_, _ = io.WriteString(stderr, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q; see beszelscope --help", args[0])
	}
}

const usage = `beszelscope is a read-only MCP server for a Beszel hub.

Usage:
  beszelscope                serve MCP over stdin and stdout
  beszelscope token ...      manage the hub token in the Keychain (macOS); see token --help
  beszelscope version        print the version

Environment:
  BESZELSCOPE_URL                  hub URL, required
  BESZELSCOPE_TOKEN                hub token, when it is not in the Keychain
  BESZELSCOPE_ALLOW_INSECURE_HTTP  1 or true to allow http to a non-loopback hub
  BESZELSCOPE_LOG_LEVEL            debug, info (default), warn or error
  OTEL_EXPORTER_OTLP_ENDPOINT      turns on OTLP/HTTP trace and log export
`

// versionLine is the version with the commit and toolchain it was built from.
func versionLine() string {
	line := "beszelscope " + version
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return line
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if rev := settings["vcs.revision"]; rev != "" {
		line += " (" + rev[:min(12, len(rev))]
		if settings["vcs.modified"] == "true" {
			line += ", modified"
		}
		line += ")"
	}
	return line + " " + info.GoVersion + " " + runtime.GOOS + "/" + runtime.GOARCH
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
