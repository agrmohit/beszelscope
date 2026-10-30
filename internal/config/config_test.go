package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"strings"
	"testing"
	"time"
)

const hub = "https://beszel.example.com"

// testUserID is a fake 15-char PocketBase record ID.
const testUserID = "testuser0000001"

// exp2036 is 2036-10-27T21:24:25Z, the expiry of a real 10-year token.
const exp2036 = 2108841865

// makeToken builds an unsigned PocketBase-shaped JWT with the given claims.
func makeToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := base64.RawURLEncoding
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." +
		enc.EncodeToString(payload) + "." + enc.EncodeToString([]byte("signature"))
}

func usersToken(t *testing.T) string {
	t.Helper()
	return makeToken(t, map[string]any{
		"id": testUserID, "type": "auth", "collectionId": usersCollectionID,
		"refreshable": false, "exp": exp2036,
	})
}

// parsed is the comparable view of a Config used by the table below.
type parsed struct {
	url, source, userID string
	expires             time.Time
	level               slog.Level
}

func TestParseAccepts(t *testing.T) {
	tok := usersToken(t)
	expires := time.Unix(exp2036, 0).UTC()
	base := func(over map[string]string) map[string]string {
		m := map[string]string{"BESZELSCOPE_URL": hub}
		maps.Copy(m, over)
		return m
	}
	tests := []struct {
		name     string
		env      map[string]string
		keychain string
		want     parsed
	}{
		{
			name: "keychain token, defaults", env: base(nil), keychain: tok,
			want: parsed{hub, SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "env token when keychain is empty", env: base(map[string]string{"BESZELSCOPE_TOKEN": tok}),
			want: parsed{hub, SourceEnv, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "trailing slash trimmed", env: map[string]string{"BESZELSCOPE_URL": hub + "/"}, keychain: tok,
			want: parsed{hub, SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "base path kept, slashes trimmed", env: map[string]string{"BESZELSCOPE_URL": hub + "/beszel//"},
			keychain: tok,
			want:     parsed{hub + "/beszel", SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "explicit port", env: map[string]string{"BESZELSCOPE_URL": "https://10.0.0.5:8090"}, keychain: tok,
			want: parsed{"https://10.0.0.5:8090", SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "http to localhost needs no opt-in", env: map[string]string{"BESZELSCOPE_URL": "http://localhost:8090"},
			keychain: tok,
			want:     parsed{"http://localhost:8090", SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "http to 127.0.0.1 needs no opt-in", env: map[string]string{"BESZELSCOPE_URL": "http://127.0.0.1:8090"},
			keychain: tok,
			want:     parsed{"http://127.0.0.1:8090", SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "http to ::1 needs no opt-in", env: map[string]string{"BESZELSCOPE_URL": "http://[::1]:8090"},
			keychain: tok,
			want:     parsed{"http://[::1]:8090", SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name:     "http to remote with opt-in 1",
			env:      map[string]string{"BESZELSCOPE_URL": "http://nas.lan:8090", "BESZELSCOPE_ALLOW_INSECURE_HTTP": "1"},
			keychain: tok,
			want:     parsed{"http://nas.lan:8090", SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name:     "http to remote with opt-in true",
			env:      map[string]string{"BESZELSCOPE_URL": "http://nas.lan:8090", "BESZELSCOPE_ALLOW_INSECURE_HTTP": "true"},
			keychain: tok,
			want:     parsed{"http://nas.lan:8090", SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "opt-in set but https anyway", env: base(map[string]string{"BESZELSCOPE_ALLOW_INSECURE_HTTP": "1"}),
			keychain: tok,
			want:     parsed{hub, SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name: "log level debug", env: base(map[string]string{"BESZELSCOPE_LOG_LEVEL": "debug"}), keychain: tok,
			want: parsed{hub, SourceKeychain, testUserID, expires, slog.LevelDebug},
		},
		{
			name: "log level warn", env: base(map[string]string{"BESZELSCOPE_LOG_LEVEL": "warn"}), keychain: tok,
			want: parsed{hub, SourceKeychain, testUserID, expires, slog.LevelWarn},
		},
		{
			name: "log level error", env: base(map[string]string{"BESZELSCOPE_LOG_LEVEL": "error"}), keychain: tok,
			want: parsed{hub, SourceKeychain, testUserID, expires, slog.LevelError},
		},
		{
			name: "expired token still parses; expiry is judged later",
			env:  base(nil),
			keychain: makeToken(t, map[string]any{
				"id": testUserID, "type": "auth", "collectionId": usersCollectionID, "exp": 1000,
			}),
			want: parsed{hub, SourceKeychain, testUserID, time.Unix(1000, 0).UTC(), slog.LevelInfo},
		},
		{
			// base64url uses - and _; a real token carries both.
			name: "token with - and _ accepted", env: base(nil), keychain: tok + "-_",
			want: parsed{hub, SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
		{
			name:     "unrelated env vars ignored",
			env:      base(map[string]string{"HOME": "/x", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318"}),
			keychain: tok,
			want:     parsed{hub, SourceKeychain, testUserID, expires, slog.LevelInfo},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Parse(tt.env, tt.keychain)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			got := parsed{cfg.HubURL.String(), cfg.TokenSource, cfg.TokenUserID, cfg.TokenExpiresAt, cfg.LogLevel}
			if got != tt.want {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	tok := usersToken(t)
	full := tok
	truncated := tok[:128]
	withUserinfo := (&url.URL{Scheme: "https", Host: "h", User: url.UserPassword("u", "p")}).String()
	tests := []struct {
		name     string
		env      map[string]string
		keychain string
		wantErr  []string // every substring must appear in the error
	}{
		{name: "nil env", env: nil, keychain: tok, wantErr: []string{"BESZELSCOPE_URL"}},
		{name: "empty env", env: map[string]string{}, keychain: tok, wantErr: []string{"BESZELSCOPE_URL"}},
		{name: "empty URL", env: map[string]string{"BESZELSCOPE_URL": ""}, keychain: tok, wantErr: []string{"BESZELSCOPE_URL"}},
		{name: "ftp scheme", env: map[string]string{"BESZELSCOPE_URL": "ftp://h"}, keychain: tok, wantErr: []string{"scheme must be https or http"}},
		{name: "no scheme", env: map[string]string{"BESZELSCOPE_URL": "beszel.example.com"}, keychain: tok, wantErr: []string{"scheme must be https or http"}},
		{name: "no host", env: map[string]string{"BESZELSCOPE_URL": "https://"}, keychain: tok, wantErr: []string{"missing host"}},
		{name: "userinfo", env: map[string]string{"BESZELSCOPE_URL": withUserinfo}, keychain: tok, wantErr: []string{"credentials in the URL"}},
		{name: "query", env: map[string]string{"BESZELSCOPE_URL": hub + "?x=1"}, keychain: tok, wantErr: []string{"query and fragment"}},
		{name: "fragment", env: map[string]string{"BESZELSCOPE_URL": hub + "#x"}, keychain: tok, wantErr: []string{"query and fragment"}},
		{name: "unparseable URL", env: map[string]string{"BESZELSCOPE_URL": "https://h:bad port"}, keychain: tok, wantErr: []string{"BESZELSCOPE_URL"}},
		{name: "http remote without opt-in", env: map[string]string{"BESZELSCOPE_URL": "http://nas.lan"}, keychain: tok, wantErr: []string{"clear text"}},
		{name: "http to private IP without opt-in", env: map[string]string{"BESZELSCOPE_URL": "http://10.0.0.5:8090"}, keychain: tok, wantErr: []string{"clear text"}},
		{name: "http to localhost-ish name", env: map[string]string{"BESZELSCOPE_URL": "http://localhost.evil.com"}, keychain: tok, wantErr: []string{"clear text"}},
		{
			name: "opt-in yes is not accepted",
			env:  map[string]string{"BESZELSCOPE_URL": hub, "BESZELSCOPE_ALLOW_INSECURE_HTTP": "yes"}, keychain: tok,
			wantErr: []string{"want 1 or true"},
		},
		{
			name: "opt-in TRUE is not accepted",
			env:  map[string]string{"BESZELSCOPE_URL": hub, "BESZELSCOPE_ALLOW_INSECURE_HTTP": "TRUE"}, keychain: tok,
			wantErr: []string{"want 1 or true"},
		},
		{
			name: "opt-in 0 is not accepted",
			env:  map[string]string{"BESZELSCOPE_URL": hub, "BESZELSCOPE_ALLOW_INSECURE_HTTP": "0"}, keychain: tok,
			wantErr: []string{"want 1 or true"},
		},
		{
			name: "unknown log level", env: map[string]string{"BESZELSCOPE_URL": hub, "BESZELSCOPE_LOG_LEVEL": "trace"},
			keychain: tok, wantErr: []string{"BESZELSCOPE_LOG_LEVEL"},
		},
		{
			name: "upper-case log level", env: map[string]string{"BESZELSCOPE_URL": hub, "BESZELSCOPE_LOG_LEVEL": "INFO"},
			keychain: tok, wantErr: []string{"BESZELSCOPE_LOG_LEVEL"},
		},
		{name: "no token anywhere", env: map[string]string{"BESZELSCOPE_URL": hub}, wantErr: []string{"no token"}},
		{
			name: "token in both keychain and env",
			env:  map[string]string{"BESZELSCOPE_URL": hub, "BESZELSCOPE_TOKEN": full}, keychain: full,
			wantErr: []string{"both the Keychain and BESZELSCOPE_TOKEN"},
		},
		{
			// Regression: the macOS `security -w` prompt kept 128 chars of a real token.
			name: "token truncated to 128 chars by security prompt", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: truncated, wantErr: []string{"part(s), want 3", "128 chars", "pbpaste"},
		},
		{
			name: "token with two parts, other length", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: tok[:strings.LastIndex(tok, ".")], wantErr: []string{"2 part(s), want 3"},
		},
		{
			name: "token with trailing newline", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: tok + "\n", wantErr: []string{"not base64url"},
		},
		{
			name: "token wrapped in quotes", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: `"` + tok + `"`, wantErr: []string{"offset 0"},
		},
		{
			name: "token with Bearer prefix", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: "Bearer " + tok, wantErr: []string{"offset 6"},
		},
		{
			name: "payload not base64url", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: "aaa.b.ccc", wantErr: []string{"payload is not base64url"},
		},
		{
			name: "payload not JSON", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: "aaa." + base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".ccc",
			wantErr:  []string{"payload is not JSON"},
		},
		{
			name: "superuser token", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: makeToken(t, map[string]any{"id": "s1", "type": "auth", "collectionId": "pbc_3142635823", "exp": exp2036}),
			wantErr:  []string{"not users", "superuser"},
		},
		{
			name: "refresh-less non-auth token type", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: makeToken(t, map[string]any{"id": "u1", "type": "file", "collectionId": usersCollectionID, "exp": exp2036}),
			wantErr:  []string{`token type "file"`},
		},
		{
			name: "missing id", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: makeToken(t, map[string]any{"type": "auth", "collectionId": usersCollectionID, "exp": exp2036}),
			wantErr:  []string{"no id claim"},
		},
		{
			name: "missing exp", env: map[string]string{"BESZELSCOPE_URL": hub},
			keychain: makeToken(t, map[string]any{"id": "u1", "type": "auth", "collectionId": usersCollectionID}),
			wantErr:  []string{"no exp claim"},
		},
		{
			name:    "independent problems reported together",
			env:     map[string]string{"BESZELSCOPE_URL": "http://nas.lan", "BESZELSCOPE_LOG_LEVEL": "loud"},
			wantErr: []string{"clear text", "BESZELSCOPE_LOG_LEVEL", "no token"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tt.env, tt.keychain)
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

// TestParseIgnoresProcessEnv guards the caarlos0/env fallback: a nil map must
// not let the real environment leak in. Not parallel: it sets process env.
func TestParseIgnoresProcessEnv(t *testing.T) {
	tok := usersToken(t)
	t.Setenv("BESZELSCOPE_URL", hub)
	t.Setenv("BESZELSCOPE_TOKEN", tok)
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "nil map", env: nil},
		{name: "empty map", env: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.env, ""); err == nil {
				t.Fatal("Parse read the process environment; want an error for the missing URL and token")
			}
		})
	}
}

func TestSecretNeverPrints(t *testing.T) {
	const raw = "eyJ.super.secret"
	s := Secret(raw)
	wrapped := struct {
		Token Secret `json:"token"`
	}{s}
	tests := []struct {
		name   string
		render func(t *testing.T) string
	}{
		{name: "%v", render: func(*testing.T) string { return fmt.Sprintf("%v", s) }},
		{name: "%s", render: func(*testing.T) string { return fmt.Sprintf("[%s]", s) }},
		{name: "%q", render: func(*testing.T) string { return fmt.Sprintf("%q", s) }},
		{name: "%x", render: func(*testing.T) string { return fmt.Sprintf("%x", s) }},
		{name: "%#v", render: func(*testing.T) string { return fmt.Sprintf("%#v", s) }},
		{name: "%+v in struct", render: func(*testing.T) string { return fmt.Sprintf("%+v", wrapped) }},
		{name: "%#v in struct", render: func(*testing.T) string { return fmt.Sprintf("%#v", wrapped) }},
		{name: "json", render: func(t *testing.T) string {
			b, err := json.Marshal(wrapped)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			return string(b)
		}},
		{name: "slog", render: func(*testing.T) string {
			var sb strings.Builder
			slog.New(slog.NewJSONHandler(&sb, nil)).Info("x", slog.Any("token", s))
			return sb.String()
		}},
	}
	rawHex := fmt.Sprintf("%x", raw)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if out := tt.render(t); strings.Contains(out, "secret") || strings.Contains(out, rawHex) {
				t.Errorf("rendered %q: the raw value leaked", out)
			}
		})
	}
	if s.Reveal() != raw {
		t.Errorf("Reveal() = %q, want the raw value", s.Reveal())
	}
}
