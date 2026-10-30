package fixtures

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// captured is the fixed capture time; fake dates are relative to it.
var captured = time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)

type file struct {
	kind Kind
	in   string
}

// decode parses JSON with numbers kept as json.Number, so values compare exactly.
func decode(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

// run feeds files through one Sanitiser and returns the last output.
func run(t *testing.T, files []file) string {
	t.Helper()
	s := New(captured)
	var out []byte
	for _, f := range files {
		var err error
		if out, err = s.File(f.kind, []byte(f.in)); err != nil {
			t.Fatalf("File(%s): %v", f.kind, err)
		}
	}
	return string(out)
}

const systemsIn = `{"items":[{"id":"abcdefghij12345","name":"alpha-box","host":"100.100.1.2","port":"45876",
	"users":["user00000000001","user00000000002"],"info":{"cpu":3.5,"wf":{"en0":{"s":"AliceNet","r":-40}}}}]}`

func TestFileOutput(t *testing.T) {
	tests := []struct {
		name  string
		files []file
		want  string
	}{
		{
			name:  "systems: name, host, ids, ssid",
			files: []file{{KindSystems, systemsIn}},
			want: `{"items":[{"id":"f00000000000001","name":"alpha","host":"192.0.2.10","port":"45876",
				"users":["f00000000000002","f00000000000003"],"info":{"cpu":3.5,"wf":{"en0":{"s":"home-5g","r":-40}}}}]}`,
		},
		{
			name:  "systems: generic docker host kept",
			files: []file{{KindSystems, `{"items":[{"id":"abc","name":"b","host":"host.docker.internal"}]}`}},
			want:  `{"items":[{"id":"f01","name":"alpha","host":"host.docker.internal"}]}`,
		},
		{
			name: "system_details: hostname follows its system, .local kept",
			files: []file{
				{KindSystems, systemsIn},
				{KindSystemDetails, `{"items":[{"id":"abcdefghij12345","system":"abcdefghij12345","hostname":"alpha-box.local","cpu":"Apple M4 Pro"}]}`},
			},
			want: `{"items":[{"id":"f00000000000001","system":"f00000000000001","hostname":"alpha.local","cpu":"Apple M4 Pro"}]}`,
		},
		{
			name: "system_details: hostname without .local",
			files: []file{
				{KindSystems, systemsIn},
				{KindSystemDetails, `{"items":[{"id":"abcdefghij12345","system":"abcdefghij12345","hostname":"Alices-Laptop"}]}`},
			},
			want: `{"items":[{"id":"f00000000000001","system":"f00000000000001","hostname":"alpha"}]}`,
		},
		{
			name: "containers: known kept, unknown stand-in, local digest faked",
			files: []file{{KindContainers, `{"items":[
				{"id":"0529e307a4ca","name":"readeck","image":"codeberg.org/readeck/readeck:latest","ports":"8000"},
				{"id":"02ae4803c795","name":"corp-worker-1","image":"corp/app:local","ports":"127.0.0.1:9092"},
				{"id":"6c443eb2a5af","name":"authelia-valkey","image":"sha256:4abae5e3"},
				{"id":"0a0a0a0a0a0a","name":"miniflux-postgres","image":"postgres:18-alpine"}]}`}},
			want: `{"items":[
				{"id":"f00000000001","name":"readeck","image":"codeberg.org/readeck/readeck:latest","ports":"8000"},
				{"id":"f00000000002","name":"gitea","image":"gitea/gitea:latest","ports":""},
				{"id":"f00000000003","name":"authelia-valkey","image":"sha256:0000000000000000000000000000000000000000000000000000000000000001"},
				{"id":"f00000000004","name":"miniflux-postgres","image":"postgres:18-alpine"}]}`,
		},
		{
			name:  "containers: known name with :local image gets a digest",
			files: []file{{KindContainers, `{"items":[{"id":"aaaaaaaaaaaa","name":"authelia","image":"authelia:local"}]}`}},
			want:  `{"items":[{"id":"f00000000001","name":"authelia","image":"sha256:0000000000000000000000000000000000000000000000000000000000000001"}]}`,
		},
		{
			name: "containers: public app names outside the keep-list are replaced",
			files: []file{{KindContainers, `{"items":[
				{"id":"aaaaaaaaaaa1","name":"dozzle","image":"ghcr.io/amir20/dozzle:latest"},
				{"id":"aaaaaaaaaaa2","name":"jellyfin","image":"lscr.io/linuxserver/jellyfin:latest"},
				{"id":"aaaaaaaaaaa3","name":"beszel-hub","image":"ghcr.io/henrygd/beszel/beszel"}]}`}},
			want: `{"items":[
				{"id":"f00000000001","name":"gitea","image":"gitea/gitea:latest"},
				{"id":"f00000000002","name":"paperless","image":"ghcr.io/paperless-ngx/paperless-ngx:latest"},
				{"id":"f00000000003","name":"beszel-hub","image":"ghcr.io/henrygd/beszel/beszel"}]}`,
		},
		{
			name: "container_stats: names match the containers file",
			files: []file{
				{KindContainers, `{"items":[{"id":"02ae4803c795","name":"corp-worker-1","image":"corp/app:local"}]}`},
				{KindContainerStats, `{"items":[{"id":"st1","system":"s1","stats":[{"n":"corp-worker-1","c":1.5},{"n":"readeck","c":2}]}]}`},
			},
			want: `{"items":[{"id":"f02","system":"f3","stats":[{"n":"gitea","c":1.5},{"n":"readeck","c":2}]}]}`,
		},
		{
			name: "services: user instance hidden, distro units kept, unknown replaced",
			files: []file{{KindServices, `{"items":[
				{"id":"u1","name":"syncthing@alice"},{"id":"u2","name":"getty@tty1"},
				{"id":"u3","name":"systemd-fsck@dev-disk-by-uuid-7B7D-74A7"},{"id":"u4","name":"NetworkManager-wait-online"},
				{"id":"u5","name":"corp-agent"},{"id":"u6","name":"user@1000"},{"id":"u7","name":"tailscaled"}]}`}},
			want: `{"items":[
				{"id":"f1","name":"syncthing@user"},{"id":"f2","name":"getty@tty1"},
				{"id":"f3","name":"systemd-fsck@dev-disk-by-uuid-0000-0000"},{"id":"f4","name":"NetworkManager-wait-online"},
				{"id":"f5","name":"restic-backup"},{"id":"f6","name":"user@1000"},{"id":"f7","name":"fail2ban"}]}`,
		},
		{
			name: "smart: serial faked in its case",
			files: []file{{KindSmart, `{"items":[{"id":"d1","serial":"0ba02c03c4d0f614","model":"APPLE SSD"},
				{"id":"d2","serial":"WD-WX12A345","model":"WDC"}]}`}},
			want: `{"items":[{"id":"f1","serial":"f000000000000001","model":"APPLE SSD"},
				{"id":"f2","serial":"F0000000002","model":"WDC"}]}`,
		},
		{
			name: "monitors: URL host replaced, path and port kept, dns server mapped",
			files: []file{{KindMonitors, `{"items":[
				{"id":"57d8d8d0","system":"s1","target":"https://alice.dev:8443/health","server":"","certInfo":{"expires":1800479782000,"issuer":"WE1"}},
				{"id":"57d8d8d1","system":"s1","target":"10.1.2.3","server":"192.168.1.1"}]}`}},
			want: `{"items":[
				{"id":"f0000001","system":"f2","target":"https://example.com:8443/health","server":"","certInfo":{"expires":1798581600000,"issuer":"WE1"}},
				{"id":"f0000003","system":"f2","target":"192.0.2.10","server":"192.0.2.11"}]}`,
		},
		{
			name:  "info: agent key replaced, version kept",
			files: []file{{KindInfo, `{"key":"ssh-ed25519 AAAArealkey","v":"0.21.1"}`}},
			want:  `{"key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFIXTUREFIXTUREFIXTUREFIXTUREFIXTUREFIXTUREFIX","v":"0.21.1"}`,
		},
		{
			name:  "system_stats: only ids change",
			files: []file{{KindSystemStats, `{"page":1,"items":[{"id":"r1","system":"s1","type":"1m","stats":{"cpu":1.25,"ni":{"eth0":[1,2,3,4]}}}]}`}},
			want:  `{"page":1,"items":[{"id":"f1","system":"f2","type":"1m","stats":{"cpu":1.25,"ni":{"eth0":[1,2,3,4]}}}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := run(t, tt.files)
			if !reflect.DeepEqual(decode(t, got), decode(t, tt.want)) {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestFileWritesOneRecordPerLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "list with envelope",
			in:   `{"page":1,"totalItems":2,"items":[{"id":"a1","x":1},{"id":"a2","x":2}]}`,
			want: "{\"page\":1,\"totalItems\":2,\"items\":[\n{\"id\":\"f1\",\"x\":1},\n{\"id\":\"f2\",\"x\":2}\n]}\n",
		},
		{
			name: "empty list",
			in:   `{"page":1,"items":[]}`,
			want: "{\"page\":1,\"items\":[\n]}\n",
		},
		{
			name: "list without envelope",
			in:   `{"items":[{"id":"a1"}]}`,
			want: "{\"items\":[\n{\"id\":\"f1\"}\n]}\n",
		},
		{
			name: "plain object",
			in:   `{"packages":[]}`,
			want: "{\"packages\":[]}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := New(captured).File(KindSystemStats, []byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestFileRejectsBadJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "truncated", in: `{"items":[`},
		{name: "not json", in: "<html>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(captured).File(KindSystems, []byte(tt.in)); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestLeaks(t *testing.T) {
	// One sanitiser that has replaced "alpha-box", "100.100.1.2", "AliceNet" and the ids.
	s := New(captured)
	if _, err := s.File(KindSystems, []byte(systemsIn)); err != nil {
		t.Fatal(err)
	}
	deny := []string{"acmecorp", "Alice"}
	tests := []struct {
		name string
		body string
		want int // number of findings
	}{
		{name: "clean", body: `{"name":"alpha","host":"192.0.2.10","ports":"127.0.0.1:8080"}`, want: 0},
		{name: "replaced name survives", body: `{"name":"alpha-box"}`, want: 1},
		{name: "replaced id survives", body: `{"system":"abcdefghij12345"}`, want: 1},
		{name: "deny word, other case", body: `{"image":"ACMECORP/api"}`, want: 1},
		{name: "deny word matches inside replaced original", body: `{"s":"AliceNet"}`, want: 2},
		{name: "10/8", body: `"10.0.0.5"`, want: 1},
		{name: "172.16/12", body: `"172.20.1.1"`, want: 1},
		{name: "172.32 is public", body: `"172.32.1.1"`, want: 0},
		{name: "192.168/16", body: `"192.168.1.10"`, want: 1},
		{name: "100.64/10 cgnat", body: `"100.64.0.1"`, want: 1},
		{name: "100.127 cgnat edge", body: `"100.127.255.1"`, want: 1},
		{name: "100.128 is public", body: `"100.128.0.1"`, want: 0},
		{name: "replaced cgnat host survives", body: `"100.100.1.2"`, want: 2},
		{name: "mac address", body: `"a4:83:e7:12:34:56"`, want: 1},
		{name: "email", body: `"someone@example.org"`, want: 1},
		{name: "unit instance is not an email", body: `"syncthing@user"`, want: 0},
		{name: "short originals ignored", body: `"r1"`, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := s.Leaks(map[string][]byte{"f.json": []byte(tt.body)}, deny)
			if len(got) != tt.want {
				t.Errorf("got %d findings %q, want %d", len(got), got, tt.want)
			}
		})
	}
}

// TestLeaksSkipsKeptValues: a public image kept for one container is not a
// leak just because another container's copy of it was replaced.
func TestLeaksSkipsKeptValues(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{
			name: "replaced first, kept later",
			in: `{"items":[{"id":"aaaaaaaaaaa1","name":"corp-db","image":"postgres:18"},
				{"id":"aaaaaaaaaaa2","name":"miniflux-postgres","image":"postgres:18"}]}`,
			want: 0,
		},
		{
			name: "kept first, replaced later",
			in: `{"items":[{"id":"aaaaaaaaaaa1","name":"miniflux-postgres","image":"postgres:18"},
				{"id":"aaaaaaaaaaa2","name":"corp-db","image":"postgres:18"}]}`,
			want: 0,
		},
		{
			name: "replaced value inside a longer kept value",
			in: `{"items":[{"id":"aaaaaaaaaaa1","name":"corp-db","image":"postgres:18"},
				{"id":"aaaaaaaaaaa2","name":"miniflux-postgres","image":"postgres:18-alpine"}]}`,
			want: 0,
		},
		{
			name: "private image never kept still leaks",
			in:   `{"items":[{"id":"aaaaaaaaaaa1","name":"corp-db","image":"corp/db:local"}]}`,
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := New(captured)
			out, err := s.File(KindContainers, []byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{"containers.json": out}
			if tt.want > 0 {
				// Simulate the private image surviving next to a kept value.
				files["other.json"] = []byte(`"postgres:18" "corp/db:local"`)
			}
			if got := s.Leaks(files, nil); len(got) != tt.want {
				t.Errorf("got %d findings %q, want %d", len(got), got, tt.want)
			}
		})
	}
}

// TestSameOriginalSamePseudonym pins consistency: a value seen in two files
// maps to one pseudonym, so links between records survive.
func TestSameOriginalSamePseudonym(t *testing.T) {
	tests := []struct {
		name  string
		first file
		then  file
	}{
		{
			name:  "system id across systems and stats",
			first: file{KindSystems, systemsIn},
			then:  file{KindSystemStats, `{"items":[{"id":"r1","system":"abcdefghij12345"}]}`},
		},
		{
			name:  "monitor id across monitors and monitor stats",
			first: file{KindMonitors, `{"items":[{"id":"57d8d8d0","system":"abcdefghij12345","target":"x.dev"}]}`},
			then:  file{KindMonitorStats, `{"items":[{"id":"r1","monitor":"57d8d8d0","system":"abcdefghij12345"}]}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := New(captured)
			a, err := s.File(tt.first.kind, []byte(tt.first.in))
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.File(tt.then.kind, []byte(tt.then.in))
			if err != nil {
				t.Fatal(err)
			}
			first := items(decode(t, string(a)))[0]["id"]
			ref := items(decode(t, string(b)))[0]
			if ref["system"] != first && ref["monitor"] != first {
				t.Errorf("reference %v does not match first id %v", ref, first)
			}
			if bytes.Contains(b, []byte("abcdefghij12345")) || bytes.Contains(b, []byte("57d8d8d0")) {
				t.Errorf("original id survived: %s", b)
			}
		})
	}
}
