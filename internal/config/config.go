// Package config turns the environment and Keychain token into a Config.
// It never reads process state; callers pass everything in.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Token sources for [Config.TokenSource].
const (
	SourceKeychain = "keychain"
	SourceEnv      = "env"
)

// Config is the validated startup configuration.
type Config struct {
	HubURL         *url.URL // no trailing slash; may include a base path
	Token          Secret
	TokenSource    string    // SourceKeychain or SourceEnv
	TokenUserID    string    // the token's "id" claim
	TokenExpiresAt time.Time // checked later; an expired token still parses
	LogLevel       slog.Level
}

type rawEnv struct {
	URL               string `env:"BESZELSCOPE_URL,required,notEmpty"`
	Token             string `env:"BESZELSCOPE_TOKEN"`               // used when the Keychain has none
	AllowInsecureHTTP string `env:"BESZELSCOPE_ALLOW_INSECURE_HTTP"` // "1" or "true"
	LogLevel          string `env:"BESZELSCOPE_LOG_LEVEL" envDefault:"info"`
}

// Parse validates env and the Keychain token (empty if none) into a Config.
// It reports every problem at once.
func Parse(env map[string]string, keychainToken string) (Config, error) {
	raw, err := parseEnv(env)
	if err != nil {
		return Config{}, err
	}

	var errs []error
	allowInsecure, err := parseFlag("BESZELSCOPE_ALLOW_INSECURE_HTTP", raw.AllowInsecureHTTP)
	errs = append(errs, err)
	hubURL, err := parseHubURL(raw.URL, allowInsecure)
	errs = append(errs, err)
	level, err := parseLogLevel(raw.LogLevel)
	errs = append(errs, err)
	tok, source, err := pickToken(keychainToken, raw.Token)
	errs = append(errs, err)

	var c claims
	if tok != "" {
		c, err = parseToken(tok)
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}

	return Config{
		HubURL:         hubURL,
		Token:          Secret(tok),
		TokenSource:    source,
		TokenUserID:    c.ID,
		TokenExpiresAt: c.expiry(),
		LogLevel:       level,
	}, nil
}

func parseEnv(vars map[string]string) (rawEnv, error) {
	// A nil map makes caarlos0/env read the real environment.
	if vars == nil {
		vars = map[string]string{}
	}
	raw, err := env.ParseAsWithOptions[rawEnv](env.Options{Environment: vars})
	if err != nil {
		return rawEnv{}, fmt.Errorf("read environment: %w", err)
	}
	return raw, nil
}

func parseFlag(name, v string) (bool, error) {
	switch v {
	case "":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("%s=%q: want 1 or true, or leave it unset", name, v)
	}
}

func parseHubURL(raw string, allowInsecure bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("BESZELSCOPE_URL: %w", err)
	}
	switch {
	case u.Scheme != "https" && u.Scheme != "http":
		return nil, fmt.Errorf("BESZELSCOPE_URL %q: scheme must be https or http", raw)
	case u.Host == "":
		return nil, fmt.Errorf("BESZELSCOPE_URL %q: missing host", raw)
	case u.User != nil:
		return nil, errors.New("BESZELSCOPE_URL: credentials in the URL are not allowed; use the token")
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("BESZELSCOPE_URL %q: query and fragment are not allowed", raw)
	}
	if u.Scheme == "http" && !allowInsecure && !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("BESZELSCOPE_URL %q: http to a non-loopback host sends the token in clear text; "+
			"use https or set BESZELSCOPE_ALLOW_INSECURE_HTTP=1", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseLogLevel(v string) (slog.Level, error) {
	switch v {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("BESZELSCOPE_LOG_LEVEL=%q: want debug, info, warn or error", v)
	}
}

func pickToken(keychain, envToken string) (tok, source string, err error) {
	switch {
	case keychain != "" && envToken != "":
		return "", "", errors.New("token set in both the Keychain and BESZELSCOPE_TOKEN; keep exactly one")
	case keychain != "":
		return keychain, SourceKeychain, nil
	case envToken != "":
		return envToken, SourceEnv, nil
	default:
		return "", "", errors.New("no token: add one to the Keychain (service beszelscope, account = hub host) " +
			"or set BESZELSCOPE_TOKEN")
	}
}
