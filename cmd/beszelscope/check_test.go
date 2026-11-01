package main

import (
	"bytes"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"
)

// result is what one token command left behind.
type result struct {
	err            error
	stdout, stderr string
	stored         map[string]string
}

// check fails the test unless r matches: wantErr "" means no error, and
// every want string must appear in its stream.
func (r result) check(t *testing.T, wantErr string, wantOut, wantErrOut []string) {
	t.Helper()
	switch {
	case wantErr == "" && r.err != nil:
		t.Fatalf("err = %v, want nil\nstdout: %s\nstderr: %s", r.err, r.stdout, r.stderr)
	case wantErr != "" && (r.err == nil || !strings.Contains(r.err.Error(), wantErr)):
		t.Fatalf("err = %v, want it to contain %q", r.err, wantErr)
	}
	for _, s := range wantOut {
		if !strings.Contains(r.stdout, s) {
			t.Fatalf("stdout lacks %q:\n%s", s, r.stdout)
		}
	}
	for _, s := range wantErrOut {
		if !strings.Contains(r.stderr, s) {
			t.Fatalf("stderr lacks %q:\n%s", s, r.stderr)
		}
	}
}

// runCmd runs beszelscope with args and reports what it left behind.
func runCmd(t *testing.T, args []string, env map[string]string, stdin string, store *fakeStore) result {
	t.Helper()
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	var tokens tokenStore
	if store != nil {
		tokens = store
	}
	err := run(t.Context(), args, environ, strings.NewReader(stdin), &stdout, &stderr, tokens)
	r := result{err: err, stdout: stdout.String(), stderr: stderr.String()}
	if store != nil {
		r.stored = maps.Clone(store.tokens)
	}
	return r
}

func hubEnv(t *testing.T, status int) map[string]string {
	t.Helper()
	return map[string]string{"BESZELSCOPE_URL": fakeHub(t, status), "BESZELSCOPE_ALLOW_INSECURE_HTTP": "1"}
}

func TestCheck(t *testing.T) {
	good := makeToken(t, time.Now().Add(365*24*time.Hour+time.Hour))
	expired := makeToken(t, time.Now().Add(-2*24*time.Hour-time.Hour))
	withToken := func(env map[string]string, tok string) map[string]string {
		env["BESZELSCOPE_TOKEN"] = tok
		return env
	}
	tests := []struct {
		name       string
		env        map[string]string
		store      *fakeStore
		noKeychain bool
		args       []string
		wantErr    string
		wantOut    []string
	}{
		{
			name: "keychain token, hub ok", env: hubEnv(t, 200), store: &fakeStore{tokens: map[string]string{"127.0.0.1": good}},
			wantOut: []string{"source   Keychain (service beszelscope, account 127.0.0.1)", "user     testuser0000001", "365 days left", "check    ok, Beszel 0.21.1"},
		},
		{
			name: "env token, hub ok", env: withToken(hubEnv(t, 200), good), store: &fakeStore{},
			wantOut: []string{"source   BESZELSCOPE_TOKEN", "check    ok"},
		},
		{
			name: "expired token, hub rejects it", env: hubEnv(t, 401), store: &fakeStore{tokens: map[string]string{"127.0.0.1": expired}},
			wantErr: "the hub check failed", wantOut: []string{"expired", "(2 days ago)", "check    failed"},
		},
		{
			name: "hub down", env: hubEnv(t, 0), store: &fakeStore{tokens: map[string]string{"127.0.0.1": good}},
			wantErr: "the hub check failed", wantOut: []string{"check    failed", "unreachable"},
		},
		{name: "no token", env: hubEnv(t, 200), store: &fakeStore{}, wantErr: "no token: add one"},
		{
			name: "keychain and env both", env: withToken(hubEnv(t, 200), good), store: &fakeStore{tokens: map[string]string{"127.0.0.1": good}},
			wantErr: "both the Keychain and BESZELSCOPE_TOKEN",
		},
		{name: "keychain fails", env: hubEnv(t, 200), store: &fakeStore{err: errors.New("locked")}, wantErr: "keychain: locked"},
		{
			name: "env token, no keychain", env: withToken(hubEnv(t, 200), good), noKeychain: true,
			wantOut: []string{"source   BESZELSCOPE_TOKEN", "365 days left", "check    ok, Beszel 0.21.1"},
		},
		{name: "no token, no keychain", env: hubEnv(t, 200), noKeychain: true, wantErr: "no token: add one"},
		{name: "stray argument", env: hubEnv(t, 200), args: []string{"now"}, wantErr: `check takes no arguments, got ["now"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			if tt.noKeychain {
				store = nil
			}
			runCmd(t, append([]string{"check"}, tt.args...), tt.env, "", store).check(t, tt.wantErr, tt.wantOut, nil)
		})
	}
}

func TestTopLevel(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantOut    string // stdout prefix
		wantOutHas string
	}{
		{name: "version", args: []string{"version"}, wantOut: "beszelscope dev"},
		{name: "--version", args: []string{"--version"}, wantOut: "beszelscope dev"},
		{name: "--help", args: []string{"--help"}, wantOut: "beszelscope is", wantOutHas: "BESZELSCOPE_URL"},
		{name: "-h", args: []string{"-h"}, wantOut: "beszelscope is", wantOutHas: "beszelscope check"},
		{name: "help", args: []string{"help"}, wantOut: "beszelscope is", wantOutHas: "Usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := run(t.Context(), tt.args, nil, strings.NewReader(""), &stdout, &stderr, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if !strings.HasPrefix(stdout.String(), tt.wantOut) || !strings.Contains(stdout.String(), tt.wantOutHas) || stderr.Len() > 0 {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}
