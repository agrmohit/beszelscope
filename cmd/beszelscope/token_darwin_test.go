package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/agrmohit/beszelscope/cmd/internal/keychain"
)

func TestTokenSet(t *testing.T) {
	good := makeToken(t, time.Now().Add(365*24*time.Hour+time.Hour))
	other := makeToken(t, time.Now().Add(30*24*time.Hour+time.Hour))
	tests := []struct {
		name       string
		env        map[string]string
		stdin      string
		before     map[string]string
		storeErr   error
		want       map[string]string // the store afterwards
		wantErr    string
		wantOut    []string
		wantErrOut []string
	}{
		{
			name: "piped token, hub accepts it", env: hubEnv(t, 200), stdin: good,
			want:    map[string]string{"127.0.0.1": good},
			wantOut: []string{"hub accepted the token (Beszel 0.21.1)", "stored the token for 127.0.0.1", "user testuser0000001", "365 days left"},
		},
		{
			name: "pbpaste with newline and spaces", env: hubEnv(t, 200), stdin: "  " + good + "\n",
			want: map[string]string{"127.0.0.1": good},
		},
		{
			name: "replaces an old token", env: hubEnv(t, 200), stdin: good,
			before: map[string]string{"127.0.0.1": other, "other.example.com": other},
			want:   map[string]string{"127.0.0.1": good, "other.example.com": other},
		},
		{
			name: "hub down: warn and store", env: hubEnv(t, 0), stdin: good,
			want:       map[string]string{"127.0.0.1": good},
			wantErrOut: []string{"warning: the hub check failed, storing anyway"},
		},
		{
			name: "hub rejects it: warn and store", env: hubEnv(t, 401), stdin: good,
			want:       map[string]string{"127.0.0.1": good},
			wantErrOut: []string{"warning: the hub check failed, storing anyway", "rejected the token"},
		},
		{
			name:  "env token also set: store and warn",
			env:   map[string]string{"BESZELSCOPE_URL": fakeHub(t, 200), "BESZELSCOPE_ALLOW_INSECURE_HTTP": "1", "BESZELSCOPE_TOKEN": other},
			stdin: good, want: map[string]string{"127.0.0.1": good},
			wantErrOut: []string{"BESZELSCOPE_TOKEN is also set"},
		},
		{
			name: "expired token", env: hubEnv(t, 200), stdin: makeToken(t, time.Now().Add(-time.Hour)),
			wantErr: "not stored: the token expired",
		},
		{name: "superuser token", env: hubEnv(t, 200), stdin: superuserToken(t), wantErr: "not stored: token is for collection"},
		{
			name: "truncated by the security prompt", env: hubEnv(t, 200), stdin: good[:128],
			wantErr: "beszelscope token set",
		},
		{name: "empty stdin", env: hubEnv(t, 200), stdin: "", wantErr: "no token on stdin"},
		{name: "only whitespace", env: hubEnv(t, 200), stdin: " \n\t\n", wantErr: "no token on stdin"},
		{name: "no URL", env: map[string]string{}, stdin: good, wantErr: "BESZELSCOPE_URL"},
		{
			name: "http to a remote hub", env: map[string]string{"BESZELSCOPE_URL": "http://nas.lan"}, stdin: good,
			wantErr: "clear text",
		},
		{
			name: "store fails", env: hubEnv(t, 200), stdin: good, storeErr: errors.New("exit 51"),
			wantErr: "store: exit 51",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{tokens: maps.Clone(tt.before), err: tt.storeErr}
			r := runToken(t, []string{"set"}, tt.env, tt.stdin, store)
			r.check(t, tt.wantErr, tt.wantOut, tt.wantErrOut)
			want := tt.want
			if want == nil {
				want = tt.before
			}
			if !maps.Equal(r.stored, want) {
				t.Fatalf("store = %v, want %v", r.stored, want)
			}
		})
	}
}

func TestReadToken(t *testing.T) {
	tests := []struct {
		name       string
		stdin      string
		readErr    bool // stdin fails after stdin's bytes
		prompt     bool
		want       string
		wantPrompt bool
		wantErr    string
	}{
		{name: "pipe", stdin: "a.b.c", want: "a.b.c"},
		{name: "pipe with newline", stdin: "a.b.c\n", want: "a.b.c"},
		{name: "pipe keeps every line", stdin: "a.b.c\nd", want: "a.b.c\nd"},
		{name: "prompt reads one line", stdin: "a.b.c\nnext\n", prompt: true, want: "a.b.c", wantPrompt: true},
		{name: "prompt without newline", stdin: "a.b.c", prompt: true, want: "a.b.c", wantPrompt: true},
		{name: "prompt, empty line", stdin: "\n", prompt: true, wantPrompt: true, wantErr: "no token on stdin"},
		{name: "pipe, empty", stdin: "", wantErr: "no token on stdin"},
		// A read that fails partway must not hand back a cut-short token.
		{name: "pipe read fails", stdin: "a.b", readErr: true, wantErr: "read token"},
		{name: "prompt read fails", stdin: "a.b", readErr: true, prompt: true, wantPrompt: true, wantErr: "read token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			var stdin io.Reader = strings.NewReader(tt.stdin)
			if tt.readErr {
				stdin = io.MultiReader(stdin, iotest.ErrReader(errors.New("broken pipe")))
			}
			got, err := readToken(stdin, &stderr, tt.prompt)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
			if prompted := strings.Contains(stderr.String(), "Paste the token"); prompted != tt.wantPrompt {
				t.Fatalf("prompted = %v, want %v", prompted, tt.wantPrompt)
			}
		})
	}
}

func TestTokenList(t *testing.T) {
	exp := time.Now().Add(10*24*time.Hour + time.Hour)
	good := makeToken(t, exp)
	tests := []struct {
		name    string
		store   *fakeStore
		wantErr string
		want    string // all of stdout
	}{
		{name: "none", store: &fakeStore{}, want: "no tokens stored\n"},
		{
			name: "one", store: &fakeStore{tokens: map[string]string{"a.example.com": good}},
			want: "a.example.com  expires " + time.Unix(exp.Unix(), 0).UTC().Format(time.RFC3339) + " (10 days left)\n",
		},
		{
			name:  "unreadable token",
			store: &fakeStore{tokens: map[string]string{"b.example.com": "abc"}},
			want:  "b.example.com  unreadable: token has 1 part(s), want 3 (header.payload.signature); it is 3 chars long\n",
		},
		{name: "keychain fails", store: &fakeStore{err: errors.New("locked")}, wantErr: "list: locked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runToken(t, []string{"list"}, nil, "", tt.store)
			r.check(t, tt.wantErr, nil, nil)
			if r.stdout != tt.want {
				t.Fatalf("stdout = %q\nwant     %q", r.stdout, tt.want)
			}
		})
	}
}

func TestTokenDelete(t *testing.T) {
	tok := makeToken(t, time.Now().Add(time.Hour))
	tests := []struct {
		name    string
		env     map[string]string
		store   *fakeStore
		want    map[string]string
		wantErr string
		wantOut []string
	}{
		{
			name: "deletes only this hub", env: map[string]string{"BESZELSCOPE_URL": "https://a.example.com"},
			store: &fakeStore{tokens: map[string]string{"a.example.com": tok, "b.example.com": tok}},
			want:  map[string]string{"b.example.com": tok}, wantOut: []string{"deleted the token for a.example.com"},
		},
		{
			name: "URL with port and path", env: map[string]string{"BESZELSCOPE_URL": "https://a.example.com:8443/beszel"},
			store: &fakeStore{tokens: map[string]string{"a.example.com": tok}}, want: map[string]string{},
		},
		{
			name: "nothing stored", env: map[string]string{"BESZELSCOPE_URL": "https://a.example.com"},
			store: &fakeStore{tokens: map[string]string{}}, want: map[string]string{}, wantErr: "no token stored for a.example.com",
		},
		{
			name: "no URL", env: map[string]string{}, store: &fakeStore{tokens: map[string]string{"a.example.com": tok}},
			want: map[string]string{"a.example.com": tok}, wantErr: "set BESZELSCOPE_URL",
		},
		{
			name: "keychain fails", env: map[string]string{"BESZELSCOPE_URL": "https://a.example.com"},
			store: &fakeStore{tokens: map[string]string{"a.example.com": tok}, err: errors.New("locked")},
			want:  map[string]string{"a.example.com": tok}, wantErr: "delete: locked",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runToken(t, []string{"delete"}, tt.env, "", tt.store)
			r.check(t, tt.wantErr, tt.wantOut, nil)
			if !maps.Equal(r.stored, tt.want) {
				t.Fatalf("store = %v, want %v", r.stored, tt.want)
			}
		})
	}
}

func TestTokenDispatch(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantErr   string
		wantOut   []string
		wantNoOut bool // stdout must stay empty
	}{
		{name: "no subcommand prints usage", args: nil, wantOut: []string{"beszelscope token set"}},
		{name: "--help", args: []string{"--help"}, wantOut: []string{"No command prints a token."}},
		{name: "-h", args: []string{"-h"}, wantOut: []string{"beszelscope token list"}},
		{name: "help", args: []string{"help"}, wantOut: []string{"beszelscope token list"}},
		{name: "set --help", args: []string{"set", "--help"}, wantOut: []string{"beszelscope token set"}},
		{name: "status moved to check", args: []string{"status"}, wantErr: "token status is now beszelscope check", wantNoOut: true},
		{name: "unknown subcommand", args: []string{"get"}, wantErr: `unknown token command "get"`, wantNoOut: true},
		{name: "unknown flag", args: []string{"list", "-x"}, wantErr: "token list: flag provided but not defined", wantNoOut: true},
		{name: "extra argument", args: []string{"delete", "a.example.com"}, wantErr: "token delete takes no arguments", wantNoOut: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runToken(t, tt.args, nil, "", &fakeStore{})
			r.check(t, tt.wantErr, tt.wantOut, nil)
			if r.stderr != "" {
				t.Fatalf("stderr = %q, want nothing: errors are returned, help goes to stdout", r.stderr)
			}
			if tt.wantNoOut && r.stdout != "" {
				t.Fatalf("stdout = %q, want nothing", r.stdout)
			}
		})
	}
}

func superuserToken(t *testing.T) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"id": "su", "type": "auth", "collectionId": "pbc_3142635823", "exp": time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + enc.EncodeToString(claims) + ".c2ln"
}

// runToken runs `beszelscope token args...`.
func runToken(t *testing.T, args []string, env map[string]string, stdin string, store *fakeStore) result {
	t.Helper()
	return runCmd(t, append([]string{"token"}, args...), env, stdin, store)
}

// keychainStore turns the Keychain's not-found exit into found=false, and
// passes every other failure on as an error.
func TestKeychainStoreGet(t *testing.T) {
	tests := []struct {
		name      string
		res       keychain.Result
		runErr    error
		want      string
		wantFound bool
		wantErr   bool
	}{
		{name: "stored", res: keychain.Result{Stdout: "a.b.c\n"}, want: "a.b.c", wantFound: true},
		{name: "not found", res: keychain.Result{Code: 44, Stderr: "could not be found"}},
		{name: "locked", res: keychain.Result{Code: 51, Stderr: "user interaction is not allowed"}, wantErr: true},
		{name: "did not run", runErr: errors.New("killed"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(context.Context, string, ...string) (keychain.Result, error) { return tt.res, tt.runErr }
			got, found, err := keychainStore{keychain.New(run)}.Get(t.Context(), "beszel.example.com")
			if (err != nil) != tt.wantErr || got != tt.want || found != tt.wantFound {
				t.Fatalf("Get = %q, %v, %v; want %q, %v, error %v", got, found, err, tt.want, tt.wantFound, tt.wantErr)
			}
		})
	}
}
