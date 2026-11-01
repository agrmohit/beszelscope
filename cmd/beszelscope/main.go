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
)

var version = "dev" // set by make build

func main() {
	info, _ := debug.ReadBuildInfo()
	version = buildVersion(version, info)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr, systemKeychain())
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "beszelscope:", err)
		os.Exit(1)
	}
}

// tokenStore holds hub tokens by hub host: the macOS Keychain
// (keychainStore), or nil where there is none (systemKeychain).
type tokenStore interface {
	Get(ctx context.Context, account string) (string, error) // "" when there is none
	Set(ctx context.Context, account, token string) error
	Delete(ctx context.Context, account string) error
	List(ctx context.Context) ([]string, error)
	Where(account string) string // names the item, for check
}

// run is the whole program. environ is in os.Environ form; tokens is nil
// where there is no Keychain.
func run(ctx context.Context, args, environ []string, stdin io.Reader, stdout, stderr io.Writer, tokens tokenStore) error {
	if len(args) == 0 {
		return serve(ctx, envMap(environ), stdin, stdout, stderr, tokens)
	}
	switch args[0] {
	case "check":
		return checkCmd(ctx, args[1:], envMap(environ), stdout, tokens)
	case "token":
		return tokenCmd(ctx, args[1:], envMap(environ), stdin, stdout, stderr, tokens)
	case "version", "--version":
		if len(args) > 1 {
			return fmt.Errorf("%s takes no arguments, got %q", args[0], args[1:])
		}
		info, _ := debug.ReadBuildInfo()
		_, _ = io.WriteString(stdout, versionLine(info)+"\n")
		return nil
	case "-h", "--help", "help":
		if len(args) > 1 {
			return fmt.Errorf("%s takes no arguments, got %q", args[0], args[1:])
		}
		_, _ = io.WriteString(stdout, usageHead+platformUsage+usageTail)
		return nil
	default:
		return errUnknownCommand(args[0])
	}
}

func errUnknownCommand(name string) error {
	return fmt.Errorf("unknown command %q; see beszelscope --help", name)
}

// The help is split so platformUsage, set per OS, sits between the parts.
const usageHead = `beszelscope is a read-only MCP server for a Beszel hub.

Usage:
  beszelscope                serve MCP over stdin and stdout
  beszelscope check          show the hub and token, and check them with the hub
`

const usageTail = `  beszelscope version        print the version (also --version)

Environment:
  BESZELSCOPE_URL                  hub URL, required
  BESZELSCOPE_TOKEN                hub token, when it is not in the Keychain
  BESZELSCOPE_ALLOW_INSECURE_HTTP  1 or true to allow http to a non-loopback hub
  BESZELSCOPE_LOG_LEVEL            debug, info (default), warn or error
  OTEL_EXPORTER_OTLP_ENDPOINT      turns on OTLP/HTTP trace and log export
`

// versionLine is the version with the commit and toolchain it was built from.
func versionLine(info *debug.BuildInfo) string {
	line := "beszelscope " + version
	if info == nil {
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

// buildVersion is the stamped version, or the module version for a
// `go install ...@v0.1.0` build, which has no ldflags stamp.
func buildVersion(ldflag string, info *debug.BuildInfo) string {
	if ldflag != "dev" || info == nil {
		return ldflag
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return ldflag
}
