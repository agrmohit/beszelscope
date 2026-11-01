package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/agrmohit/beszelscope/internal/config"
)

// quiet drops the hub client's request logs: check and the token commands
// report to the terminal in plain words, not as JSON log lines.
var quiet = slog.New(slog.DiscardHandler)

// checkCmd runs `beszelscope check`: the hub, where the token comes from,
// when it expires, and whether the hub accepts it.
func checkCmd(ctx context.Context, args []string, env map[string]string, stdout io.Writer, tokens tokenStore) error {
	if len(args) > 0 {
		return fmt.Errorf("check takes no arguments, got %q", args)
	}
	stored, err := storedToken(ctx, tokens, env["BESZELSCOPE_URL"])
	if err != nil {
		return err
	}
	cfg, err := config.Parse(env, stored)
	if err != nil {
		return err
	}
	source := "BESZELSCOPE_TOKEN"
	if cfg.TokenSource == config.SourceKeychain {
		source = tokens.Where(cfg.HubURL.Hostname())
	}
	_, _ = fmt.Fprintf(stdout, "hub      %s\nsource   %s\nuser     %s\ntoken    %s\n",
		cfg.HubURL, source, cfg.TokenUserID, expiry(time.Now(), cfg.TokenExpiresAt))

	info, err := newHubClient(cfg, noop.NewTracerProvider(), quiet).Info(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "check    failed: %v\n", err)
		return errors.New("the hub check failed")
	}
	_, _ = fmt.Fprintf(stdout, "check    ok, Beszel %s\n", info.Version)
	return nil
}

// expiry renders when a token expires, with whole days to go or gone.
func expiry(now, at time.Time) string {
	days := int(at.Sub(now).Hours() / 24)
	stamp := at.UTC().Format(time.RFC3339)
	if !at.After(now) {
		return fmt.Sprintf("expired %s (%d days ago)", stamp, -days)
	}
	return fmt.Sprintf("expires %s (%d days left)", stamp, days)
}
