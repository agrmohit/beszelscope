package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agrmohit/beszelscope/cmd/internal/keychain"
)

// makeToken builds an unsigned PocketBase user token that expires at exp.
func makeToken(t *testing.T, exp time.Time) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"id": "testuser0000001", "type": "auth", "collectionId": "_pb_users_auth_",
		"refreshable": false, "exp": exp.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + enc.EncodeToString(claims) + ".c2ln"
}

// fakeHub answers /api/beszel/info with status; 0 closes the server first.
func fakeHub(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if r.URL.Path == "/api/beszel/info" && status == http.StatusOK {
			_, _ = io.WriteString(w, `{"v":"0.21.1"}`)
			return
		}
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	t.Cleanup(srv.Close)
	if status == 0 {
		srv.Close()
	}
	return srv.URL
}

// fakeStore is a tokenStore with one token per account.
type fakeStore struct {
	tokens map[string]string
	err    error
}

func (s *fakeStore) Get(_ context.Context, account string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if tok, ok := s.tokens[account]; ok {
		return tok, nil
	}
	return "", keychain.ErrNotFound
}

func (s *fakeStore) Set(context.Context, string, string) error { return nil }
func (s *fakeStore) Delete(context.Context, string) error      { return nil }
func (s *fakeStore) List(context.Context) ([]string, error)    { return nil, nil }

// logEvents returns the msg of every JSON log line.
func logEvents(t *testing.T, logs string) []string {
	t.Helper()
	var msgs []string
	for line := range strings.Lines(logs) {
		var l struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		msgs = append(msgs, l.Msg)
	}
	return msgs
}

func TestRunErrors(t *testing.T) {
	good := makeToken(t, time.Now().Add(365*24*time.Hour))
	url := "https://beszel.example.com"
	tests := []struct {
		name    string
		args    []string
		environ []string
		store   tokenStore
		wantErr string
	}{
		{name: "unknown command", args: []string{"serve"}, wantErr: `unknown command "serve"`},
		{name: "unknown flag", args: []string{"--help"}, wantErr: `unknown command "--help"`},
		{name: "no URL", environ: []string{"BESZELSCOPE_TOKEN=" + good}, wantErr: "BESZELSCOPE_URL"},
		{name: "no token anywhere", environ: []string{"BESZELSCOPE_URL=" + url}, store: &fakeStore{}, wantErr: "no token: add one"},
		{name: "no token, no keychain", environ: []string{"BESZELSCOPE_URL=" + url}, wantErr: "no token: add one"},
		{
			name:    "keychain fails",
			environ: []string{"BESZELSCOPE_URL=" + url, "BESZELSCOPE_TOKEN=" + good},
			store:   &fakeStore{err: errors.New("no answer within 5s")}, wantErr: "keychain: no answer within 5s",
		},
		{
			name:    "token in keychain and env",
			environ: []string{"BESZELSCOPE_URL=" + url, "BESZELSCOPE_TOKEN=" + good},
			store:   &fakeStore{tokens: map[string]string{"beszel.example.com": good}}, wantErr: "both the Keychain and BESZELSCOPE_TOKEN",
		},
		{
			name:    "bad token in keychain",
			environ: []string{"BESZELSCOPE_URL=" + url},
			store:   &fakeStore{tokens: map[string]string{"beszel.example.com": "abc"}}, wantErr: "config:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run(t.Context(), tt.args, tt.environ, strings.NewReader(""), &stdout, io.Discard, tt.store)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
			}
			if stdout.Len() > 0 {
				t.Fatalf("stdout = %q, want nothing", stdout.String())
			}
		})
	}
}

func TestServeStartup(t *testing.T) {
	year := time.Now().Add(365 * 24 * time.Hour)
	tests := []struct {
		name    string
		status  int // hub answer; 0 = hub down
		expires time.Time
		store   bool // token in the Keychain rather than the env
		want    []string
	}{
		{name: "env token, hub up", status: 200, expires: year, want: []string{"startup.ready"}},
		{name: "keychain token, hub up", status: 200, expires: year, store: true, want: []string{"startup.ready"}},
		{
			name: "token expiring", status: 200, expires: time.Now().Add(10 * 24 * time.Hour),
			want: []string{"startup.token_warning", "startup.ready"},
		},
		{
			name: "token expired, hub rejects it", status: 401, expires: time.Now().Add(-time.Hour),
			want: []string{"startup.token_warning", "startup.hub_check_failed"},
		},
		{name: "hub rejects a live token", status: 401, expires: year, want: []string{"startup.hub_check_failed"}},
		{name: "hub down", status: 0, expires: year, want: []string{"startup.hub_check_failed"}},
		{name: "hub errors", status: 500, expires: year, want: []string{"startup.hub_check_failed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hubURL := fakeHub(t, tt.status)
			tok := makeToken(t, tt.expires)
			environ := []string{"BESZELSCOPE_URL=" + hubURL, "BESZELSCOPE_ALLOW_INSECURE_HTTP=1"}
			var store tokenStore
			if tt.store {
				store = &fakeStore{tokens: map[string]string{"127.0.0.1": tok}}
			} else {
				environ = append(environ, "BESZELSCOPE_TOKEN="+tok)
			}
			var stderr bytes.Buffer
			// Empty stdin ends the session at once, after startup.
			if err := run(t.Context(), nil, environ, strings.NewReader(""), io.Discard, &stderr, store); err != nil {
				t.Fatalf("run: %v", err)
			}
			var got []string
			for _, m := range logEvents(t, stderr.String()) {
				if strings.HasPrefix(m, "startup.") {
					got = append(got, m)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("startup events = %q, want %q\nlogs:\n%s", got, tt.want, stderr.String())
			}
		})
	}
}

// syncBuffer is a bytes.Buffer safe to write from the server goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p) //nolint:wrapcheck // bytes.Buffer never fails
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestServeMCP drives the real binary's run over pipes, as Claude Code does.
func TestServeMCP(t *testing.T) {
	environ := []string{
		"BESZELSCOPE_URL=" + fakeHub(t, 200), "BESZELSCOPE_ALLOW_INSECURE_HTTP=1",
		"BESZELSCOPE_TOKEN=" + makeToken(t, time.Now().Add(365*24*time.Hour)),
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	stderr := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- run(t.Context(), nil, environ, inR, outW, stderr, nil)
		_ = outW.Close()
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"fleet_status"}) {
		t.Fatalf("tools = %q", names)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "fleet_status", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	_ = cs.Close()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(stderr.String(), `"msg":"tool.call.completed"`) {
		t.Fatalf("no tool.call.completed log:\n%s", stderr.String())
	}
}

func TestServeStops(t *testing.T) {
	tests := []struct {
		name   string
		cancel bool // cancel ctx instead of closing stdin
	}{
		{name: "stdin closed"},
		{name: "context cancelled, as on SIGTERM", cancel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			environ := []string{
				"BESZELSCOPE_URL=" + fakeHub(t, 200), "BESZELSCOPE_ALLOW_INSECURE_HTTP=1",
				"BESZELSCOPE_TOKEN=" + makeToken(t, time.Now().Add(365*24*time.Hour)),
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			inR, inW := io.Pipe()
			defer func() { _ = inW.Close() }()
			done := make(chan error, 1)
			go func() { done <- run(ctx, nil, environ, inR, io.Discard, io.Discard, nil) }()
			if tt.cancel {
				cancel()
			} else {
				_ = inW.Close()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("run = %v, want nil", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("run did not return")
			}
		})
	}
}
