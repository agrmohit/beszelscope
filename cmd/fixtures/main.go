// Command fixtures captures responses from the Beszel hub, pseudonymises them
// and writes them to internal/hub/testdata/hub for tests. Run it with
// `make fixtures` from the repo root.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/agrmohit/beszelscope/cmd/internal/keychain"
	"github.com/agrmohit/beszelscope/internal/config"
	"github.com/agrmohit/beszelscope/internal/fixtures"
)

const (
	outDir   = "internal/hub/testdata/hub"
	denyFile = "fixtures.deny" // gitignored; one private word per line
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Environ(), os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixtures:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, environ []string, stderr io.Writer) error {
	env := envMap(environ)
	deny, err := readDenyWords()
	if err != nil {
		return err
	}
	token, err := keychain.New(keychain.Security).Get(ctx, hostOf(env["BESZELSCOPE_URL"]))
	if err != nil && !errors.Is(err, keychain.ErrNotFound) {
		return fmt.Errorf("keychain: %w", err)
	}
	cfg, err := config.Parse(env, token)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	c := &client{
		base:  cfg.HubURL,
		token: cfg.Token.Reveal(),
		http:  &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{ForceAttemptHTTP2: true}},
	}
	raw, meta, err := capture(ctx, c)
	if err != nil {
		return err
	}

	s := fixtures.New(meta.CapturedAt)
	out := map[string][]byte{}
	for _, r := range raw {
		b, err := s.File(r.kind, r.body)
		if err != nil {
			return fmt.Errorf("%s: %w", r.file, err)
		}
		out[r.fileName(s)] = b
	}
	meta.FullSystem = s.SystemName(meta.FullSystem)
	if out["meta.json"], err = json.MarshalIndent(meta, "", "  "); err != nil {
		return fmt.Errorf("encode meta: %w", err)
	}
	if leaks := s.Leaks(out, deny); len(leaks) > 0 {
		return fmt.Errorf("refusing to write, %d leak(s):\n  %s", len(leaks), strings.Join(leaks, "\n  "))
	}
	if err := write(outDir, out); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stderr, "wrote %d files to %s\n", len(out), outDir)
	return err //nolint:wrapcheck // terminal write in main
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

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func readDenyWords() ([]string, error) {
	b, err := os.ReadFile(denyFile)
	if err != nil {
		return nil, fmt.Errorf("read %s (one private word per line, required): %w", denyFile, err)
	}
	var words []string
	for line := range strings.Lines(string(b)) {
		if w := strings.TrimSpace(line); w != "" && !strings.HasPrefix(w, "#") {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("%s has no words", denyFile)
	}
	return words, nil
}

// write replaces every JSON file in dir with files.
func write(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	old, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return fmt.Errorf("remove old fixture: %w", err)
		}
	}
	var errs []error
	for name, b := range files {
		errs = append(errs, os.WriteFile(filepath.Join(dir, name), b, 0o600))
	}
	return errors.Join(errs...)
}
