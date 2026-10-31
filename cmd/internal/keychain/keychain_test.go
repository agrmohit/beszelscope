package keychain

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	host  = "beszel.example.com"
	token = "aaa.bbb-ccc_ddd"
)

type call struct {
	stdin string
	args  []string
}

type reply func(ctx context.Context) (Result, error)

func exit(code int, stderr string) reply {
	return func(context.Context) (Result, error) { return Result{Code: code, Stderr: stderr}, nil }
}

func stdout(s string) reply {
	return func(context.Context) (Result, error) { return Result{Stdout: s}, nil }
}

func hang(ctx context.Context) (Result, error) {
	<-ctx.Done()
	return Result{}, errors.New("killed")
}

// fake answers each security subcommand ("-i" for stdin commands) from
// replies and records every call. A subcommand without a reply succeeds.
type fake struct {
	replies map[string]reply
	calls   []call
}

func (f *fake) keychain() *Keychain {
	k := New(func(ctx context.Context, stdin string, args ...string) (Result, error) {
		f.calls = append(f.calls, call{stdin, args})
		if r, ok := f.replies[args[0]]; ok {
			return r(ctx)
		}
		return Result{}, nil
	})
	k.timeout = 20 * time.Millisecond
	return k
}

func checkErr(t *testing.T, err error, want string, wantIs error) {
	t.Helper()
	switch {
	case wantIs != nil && !errors.Is(err, wantIs):
		t.Fatalf("err = %v, want %v", err, wantIs)
	case want == "" && wantIs == nil && err != nil:
		t.Fatalf("err = %v, want nil", err)
	case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		name    string
		account string
		reply   reply
		want    string
		wantErr string
		wantIs  error
	}{
		{name: "token with trailing newline", account: host, reply: stdout(token + "\n"), want: token},
		{name: "token without newline", account: host, reply: stdout(token), want: token},
		{name: "IPv6 host", account: "::1", reply: stdout(token + "\n"), want: token},
		{name: "host with digits and dashes", account: "hub-2.lan", reply: stdout(token + "\n"), want: token},
		{name: "empty item", account: host, reply: stdout("\n"), want: ""},
		{name: "not found", account: host, reply: exit(44, "could not be found"), wantIs: ErrNotFound},
		{name: "other exit", account: host, reply: exit(51, "  user interaction is not allowed\n"), wantErr: "exit 51: user interaction is not allowed"},
		{name: "did not start", account: host, reply: func(context.Context) (Result, error) { return Result{}, errors.New("no such file") }, wantErr: "no such file"},
		{name: "hangs", account: host, reply: hang, wantErr: "no answer within 20ms; is the Keychain locked?"},
		{name: "empty account", account: "", wantErr: "want a hub host name"},
		{name: "account with space", account: "a b", wantErr: "want a hub host name"},
		{name: "account with newline", account: "a\nb", wantErr: "want a hub host name"},
		{name: "account over 253 chars", account: strings.Repeat("a", 254), wantErr: "want a hub host name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fake{replies: map[string]reply{"find-generic-password": tt.reply}}
			got, err := f.keychain().Get(t.Context(), tt.account)
			checkErr(t, err, tt.wantErr, tt.wantIs)
			if got != tt.want {
				t.Fatalf("token = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCalls(t *testing.T) {
	add := call{"add-generic-password -s beszelscope -a " + host + " -w " + token + "\n", []string{"-i"}}
	del := call{"", []string{"delete-generic-password", "-s", "beszelscope", "-a", host}}
	find := call{"", []string{"find-generic-password", "-s", "beszelscope", "-a", host, "-w"}}
	tests := []struct {
		name    string
		replies map[string]reply
		do      func(context.Context, *Keychain) error
		want    []call
	}{
		{
			name:    "set with no old item",
			replies: map[string]reply{"delete-generic-password": exit(44, "")},
			do:      func(ctx context.Context, k *Keychain) error { return k.Set(ctx, host, token) },
			want:    []call{del, add},
		},
		{
			name: "set replaces an old item",
			do:   func(ctx context.Context, k *Keychain) error { return k.Set(ctx, host, token) },
			want: []call{del, add},
		},
		{
			name: "set twice deletes each time",
			do: func(ctx context.Context, k *Keychain) error {
				if err := k.Set(ctx, host, token); err != nil {
					return err
				}
				return k.Set(ctx, host, token)
			},
			want: []call{del, add, del, add},
		},
		{
			name:    "set stops when delete fails",
			replies: map[string]reply{"delete-generic-password": exit(51, "")},
			do:      func(ctx context.Context, k *Keychain) error { return k.Set(ctx, host, token) },
			want:    []call{del},
		},
		{
			name: "get",
			do:   func(ctx context.Context, k *Keychain) error { _, err := k.Get(ctx, host); return err },
			want: []call{find},
		},
		{
			name: "delete",
			do:   func(ctx context.Context, k *Keychain) error { return k.Delete(ctx, host) },
			want: []call{del},
		},
		{
			name: "list",
			do:   func(ctx context.Context, k *Keychain) error { _, err := k.List(ctx); return err },
			want: []call{{"", []string{"dump-keychain"}}},
		},
		{
			name: "set with a token that could inject a command runs nothing",
			do:   func(ctx context.Context, k *Keychain) error { return k.Set(ctx, host, token+"\ndelete-keychain") },
		},
		{
			name: "set with a bad account runs nothing",
			do:   func(ctx context.Context, k *Keychain) error { return k.Set(ctx, "a b", token) },
		},
		{
			name: "get with a bad account runs nothing",
			do:   func(ctx context.Context, k *Keychain) error { _, err := k.Get(ctx, "a\nb"); return err },
		},
		{
			name: "delete with a bad account runs nothing",
			do:   func(ctx context.Context, k *Keychain) error { return k.Delete(ctx, "") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fake{replies: tt.replies}
			_ = tt.do(t.Context(), f.keychain())
			if !slices.EqualFunc(f.calls, tt.want, func(a, b call) bool {
				return a.stdin == b.stdin && slices.Equal(a.args, b.args)
			}) {
				t.Fatalf("calls = %q\nwant    %q", f.calls, tt.want)
			}
		})
	}
}

func TestSetErrors(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		replies map[string]reply
		wantErr string
		notErr  string // must not appear in the error
	}{
		{name: "fresh", token: token, replies: map[string]reply{"delete-generic-password": exit(44, "")}},
		{name: "replace", token: token},
		{name: "mixed case and digits", token: "AAA.bbb.123"},
		{name: "empty token", token: "", wantErr: "token may hold only"},
		{name: "token with newline", token: token + "\nx", wantErr: "token may hold only"},
		{name: "token with space", token: "a b", wantErr: "token may hold only"},
		{name: "token with quote", token: `a"b`, wantErr: "token may hold only"},
		{name: "token with Bearer prefix", token: "Bearer " + token, wantErr: "token may hold only"},
		{
			name: "delete fails", token: token,
			replies: map[string]reply{"delete-generic-password": exit(51, "denied")},
			wantErr: "exit 51: denied",
		},
		{
			name: "add fails after replacing", token: token,
			replies: map[string]reply{"-i": exit(1, "add-generic-password: returned -25308")},
			wantErr: "the old token is already deleted: security -i: exit 1",
		},
		{
			name: "add fails with no old item", token: token,
			replies: map[string]reply{"delete-generic-password": exit(44, ""), "-i": exit(1, "")},
			wantErr: "security -i: exit 1", notErr: "already deleted",
		},
		{
			name: "add finds a duplicate", token: token,
			replies: map[string]reply{"delete-generic-password": exit(44, ""), "-i": exit(45, "")},
			wantErr: "item already exists",
		},
		{
			name: "add hangs", token: token,
			replies: map[string]reply{"-i": hang},
			wantErr: "no answer within",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fake{replies: tt.replies}
			err := f.keychain().Set(t.Context(), host, tt.token)
			checkErr(t, err, tt.wantErr, nil)
			if tt.notErr != "" && strings.Contains(err.Error(), tt.notErr) {
				t.Fatalf("err = %v, want it without %q", err, tt.notErr)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name    string
		reply   reply
		wantErr string
		wantIs  error
	}{
		{name: "deleted", reply: stdout("password has been deleted.\n")},
		{name: "deleted, no output", reply: stdout("")},
		{name: "not found", reply: exit(44, ""), wantIs: ErrNotFound},
		{name: "other exit", reply: exit(51, "denied"), wantErr: "exit 51: denied"},
		{name: "hangs", reply: hang, wantErr: "no answer within"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fake{replies: map[string]reply{"delete-generic-password": tt.reply}}
			checkErr(t, f.keychain().Delete(t.Context(), host), tt.wantErr, tt.wantIs)
		})
	}
}

// item renders one entry the way `security dump-keychain` prints it.
func item(class, service, account string) string {
	return `keychain: "/Users/x/Library/Keychains/login.keychain-db"
version: 512
class: "` + class + `"
attributes:
    0x00000007 <blob>="` + service + `"
    "acct"<blob>="` + account + `"
    "cdat"<timedate>=0x32303236313033313030303431385A00  "20261031000418Z\000"
    "svce"<blob>="` + service + `"
`
}

func TestList(t *testing.T) {
	tests := []struct {
		name    string
		reply   reply
		want    []string
		wantErr string
	}{
		{name: "one item", reply: stdout(item("genp", Service, host)), want: []string{host}},
		{
			name:  "several items, sorted",
			reply: stdout(item("genp", Service, "z.example.com") + item("genp", Service, "a.example.com")),
			want:  []string{"a.example.com", "z.example.com"},
		},
		{
			name:  "other services ignored",
			reply: stdout(item("genp", "beszelscope-old", "x") + item("genp", "gh:github.com", "y") + item("genp", Service, host)),
			want:  []string{host},
		},
		{
			name:  "internet password with our service name ignored",
			reply: stdout(item("inet", Service, "x") + item("genp", Service, host)),
			want:  []string{host},
		},
		{
			name:  "same account twice listed once",
			reply: stdout(item("genp", Service, host) + item("genp", Service, host)),
			want:  []string{host},
		},
		{name: "item without account ignored", reply: stdout(item("genp", Service, ""))},
		{name: "no items", reply: stdout("")},
		{name: "fails", reply: exit(50, "keychain missing"), wantErr: "exit 50: keychain missing"},
		{name: "hangs", reply: hang, wantErr: "no answer within"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fake{replies: map[string]reply{"dump-keychain": tt.reply}}
			got, err := f.keychain().List(t.Context())
			checkErr(t, err, tt.wantErr, nil)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("accounts = %q, want %q", got, tt.want)
			}
		})
	}
}
