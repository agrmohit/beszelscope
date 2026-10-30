package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// IDs from the sanitised fixtures in testdata/hub.
const bravoID = "f00000000000004"

var since = time.Date(2026, 10, 30, 21, 0, 0, 0, time.UTC)

func newClient(t *testing.T, h http.Handler, opts ...func(*Options)) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	o := Options{
		BaseURL: u, Token: "tok", HTTP: srv.Client(),
		Timeout: 300 * time.Millisecond, LiveTimeout: 600 * time.Millisecond,
		RetryDelay: time.Millisecond, MaxParallel: 8,
	}
	for _, f := range opts {
		f(&o)
	}
	return New(o)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func serve(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }
}

// itemCount counts the records in a fixture.
func itemCount(t *testing.T, b []byte) int {
	t.Helper()
	var p struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return len(p.Items)
}

func TestDecodesFixtures(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		check func(t *testing.T, c *Client, want int)
	}{
		{"systems", "hub/systems.json", func(t *testing.T, c *Client, want int) {
			s, err := c.Systems(t.Context())
			if err != nil || len(s) != want || s[0].Name != "alpha" || s[1].Info.AgentVersion == "" {
				t.Errorf("got %d systems, err %v, first %+v", len(s), err, s)
			}
		}},
		{"system details", "hub/system_details.json", func(t *testing.T, c *Client, want int) {
			d, err := c.SystemDetails(t.Context())
			if err != nil || len(d) != want || d[0].Cores == 0 || d[0].Memory == 0 {
				t.Errorf("got %d details, err %v", len(d), err)
			}
		}},
		{"containers", "hub/containers.json", func(t *testing.T, c *Client, want int) {
			r, err := c.Containers(t.Context())
			if err != nil || len(r) != want || r[0].Name == "" || r[0].MemoryMiB == 0 {
				t.Errorf("got %d containers, err %v", len(r), err)
			}
		}},
		{"services", "hub/systemd_services.json", func(t *testing.T, c *Client, want int) {
			r, err := c.Services(t.Context())
			if err != nil || len(r) != want || r[0].Name == "" || r[0].Updated == 0 {
				t.Errorf("got %d services, err %v", len(r), err)
			}
		}},
		{"smart devices", "hub/smart_devices.json", func(t *testing.T, c *Client, want int) {
			r, err := c.SmartDevices(t.Context())
			if err != nil || len(r) != want || r[0].State != "PASSED" || len(r[0].Attributes) == 0 || r[0].Updated.IsZero() {
				t.Errorf("got %d devices, err %v", len(r), err)
			}
		}},
		{"monitors", "hub/network_monitors.json", func(t *testing.T, c *Client, want int) {
			r, err := c.Monitors(t.Context())
			if err != nil || len(r) != want || r[0].CertInfo == nil || r[0].ResAvg1h == 0 {
				t.Errorf("got %d monitors, err %v", len(r), err)
			}
		}},
		{"system stats 1m", "hub/system_stats_bravo_1m.json", func(t *testing.T, c *Client, want int) {
			r, err := c.SystemStats(t.Context(), bravoID, Stat1m, since)
			if err != nil || len(r) != want || r[0].Stats.Mem[0] == 0 || r[0].Created.IsZero() {
				t.Errorf("got %d samples, err %v", len(r), err)
			}
		}},
		{"system stats 480m rollups keep a max", "hub/system_stats_bravo_480m.json", func(t *testing.T, c *Client, want int) {
			r, err := c.SystemStats(t.Context(), bravoID, Stat480m, since)
			if err != nil || len(r) != want || r[len(r)-1].Stats.MaxCPU == 0 {
				t.Errorf("got %d samples, err %v", len(r), err)
			}
		}},
		{"container stats", "hub/container_stats_bravo_1m.json", func(t *testing.T, c *Client, want int) {
			r, err := c.ContainerStats(t.Context(), bravoID, Stat1m, since)
			if err != nil || len(r) != want || len(r[0].Stats) == 0 || r[0].Stats[0].Name == "" {
				t.Errorf("got %d samples, err %v", len(r), err)
			}
		}},
		{"monitor stats", "hub/network_monitor_stats_bravo_10m.json", func(t *testing.T, c *Client, want int) {
			r, err := c.MonitorStats(t.Context(), bravoID, Stat10m, since)
			if err != nil || len(r) != want || r[0].Created == 0 || r[0].Total == 0 {
				t.Errorf("got %d samples, err %v", len(r), err)
			}
		}},
		{"down system has no latest sample", "hub/system_stats_latest_alpha.json", func(t *testing.T, c *Client, want int) {
			r, err := c.SystemStats(t.Context(), "f00000000000001", Stat1m, since)
			if err != nil || len(r) != want || want != 0 {
				t.Errorf("got %d samples, want 0, err %v", len(r), err)
			}
		}},
		{"zfs pools", "handmade/zfs_pools.json", func(t *testing.T, c *Client, want int) {
			r, err := c.ZfsPools(t.Context())
			if err != nil || len(r) != want || r[1].Health != "DEGRADED" || r[1].Vdevs[0].ReadErrs != 3 || r[0].Scrub == nil {
				t.Errorf("got %d pools, err %v: %+v", len(r), err, r)
			}
		}},
		{"package updates", "hub/package_updates_bravo.json", func(t *testing.T, c *Client, _ int) {
			p, err := c.PackageUpdates(t.Context(), bravoID)
			if err != nil || p.Packages == nil {
				t.Errorf("got %+v, err %v", p, err)
			}
		}},
		{"info", "hub/info.json", func(t *testing.T, c *Client, _ int) {
			i, err := c.Info(t.Context())
			if err != nil || i.Version != "0.21.1" {
				t.Errorf("got %+v, err %v", i, err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := fixture(t, tt.file)
			want := 0
			if strings.Contains(string(body), `"items"`) {
				want = itemCount(t, body)
			}
			tt.check(t, newClient(t, serve(body)), want)
		})
	}
}

func TestRequestShape(t *testing.T) {
	empty := []byte(`{"items":[]}`)
	tests := []struct {
		name       string
		base       string // path prefix of the hub
		call       func(context.Context, *Client) error
		wantPath   string
		wantFilter string
		wantSort   string
		wantFields string
	}{
		{
			name: "systems", call: func(ctx context.Context, c *Client) error { _, err := c.Systems(ctx); return err },
			wantPath: "/api/collections/systems/records", wantSort: "name",
			wantFields: "id,name,host,port,status,info,updated",
		},
		{
			name: "under a base path", base: "/beszel",
			call:     func(ctx context.Context, c *Client) error { _, err := c.Containers(ctx); return err },
			wantPath: "/beszel/api/collections/containers/records", wantSort: "system,name",
			wantFields: "id,system,name,image,ports,status,health,cpu,memory,net,updated,updatable",
		},
		{
			name: "system stats with date filter",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.SystemStats(ctx, bravoID, Stat20m, since)
				return err
			},
			wantPath:   "/api/collections/system_stats/records",
			wantFilter: "system='f00000000000004' && type='20m' && created>='2026-10-30 21:00:00.000Z'",
			wantSort:   "created", wantFields: "system,type,created,stats",
		},
		{
			name: "monitor stats filter on unix ms",
			call: func(ctx context.Context, c *Client) error {
				_, err := c.MonitorStats(ctx, bravoID, Stat1m, since)
				return err
			},
			wantPath:   "/api/collections/network_monitor_stats/records",
			wantFilter: "system='f00000000000004' && type='1m' && created>=1793394000000",
			wantSort:   "created",
			wantFields: "monitor,system,type,created,total_count,success_count,res_sum,res_min,res_max",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got *http.Request
			var mu sync.Mutex
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				if got == nil && strings.Contains(r.URL.Path, "/records") {
					got = r.Clone(context.Background())
				}
				mu.Unlock()
				if strings.HasSuffix(r.URL.Path, "/api/beszel/info") {
					_, _ = w.Write([]byte(`{"v":"0.21.1"}`))
					return
				}
				_, _ = w.Write(empty)
			})
			c := newClient(t, h, func(o *Options) { o.BaseURL = o.BaseURL.JoinPath(tt.base) })
			if err := tt.call(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			q := got.URL.Query()
			checks := map[string][2]string{
				"method":        {got.Method, http.MethodGet},
				"path":          {got.URL.Path, tt.wantPath},
				"authorization": {got.Header.Get("Authorization"), "tok"},
				"filter":        {q.Get("filter"), tt.wantFilter},
				"sort":          {q.Get("sort"), tt.wantSort},
				"fields":        {q.Get("fields"), tt.wantFields},
				"perPage":       {q.Get("perPage"), "1000"},
				"skipTotal":     {q.Get("skipTotal"), "true"},
				"page":          {q.Get("page"), "1"},
			}
			for k, v := range checks {
				if v[0] != v[1] {
					t.Errorf("%s = %q, want %q", k, v[0], v[1])
				}
			}
		})
	}
}

// step is one scripted server reply: a status, a reset connection, or a stall.
type step struct {
	status int
	reset  bool
	stall  time.Duration
	body   string
}

func scripted(t *testing.T, steps []step, hits *atomic.Int32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		n := int(hits.Add(1)) - 1
		s := steps[min(n, len(steps)-1)]
		if s.reset {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		if s.stall > 0 {
			select {
			case <-time.After(s.stall):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}
}

func TestRetryPolicy(t *testing.T) {
	ok := step{status: 200, body: `{"v":"0.21.1"}`}
	tests := []struct {
		name     string
		steps    []step
		wantHits int32
		wantErr  error // nil, ErrUnreachable, ErrUnauthorized, or errStatus
	}{
		{name: "200 first time", steps: []step{ok}, wantHits: 1},
		{name: "503 then 200", steps: []step{{status: 503}, ok}, wantHits: 2},
		{name: "502 then 200", steps: []step{{status: 502}, ok}, wantHits: 2},
		{name: "504 then 200", steps: []step{{status: 504}, ok}, wantHits: 2},
		{name: "502 twice gives up", steps: []step{{status: 502}, {status: 502}}, wantHits: 2, wantErr: errStatus},
		{name: "reset then 200", steps: []step{{reset: true}, ok}, wantHits: 2},
		{name: "reset twice gives up", steps: []step{{reset: true}, {reset: true}}, wantHits: 2, wantErr: ErrUnreachable},
		{name: "500 not retried", steps: []step{{status: 500}, ok}, wantHits: 1, wantErr: errStatus},
		{name: "404 not retried", steps: []step{{status: 404}, ok}, wantHits: 1, wantErr: errStatus},
		{name: "401 not retried", steps: []step{{status: 401}, ok}, wantHits: 1, wantErr: ErrUnauthorized},
		{name: "403 not retried", steps: []step{{status: 403}, ok}, wantHits: 1, wantErr: ErrUnauthorized},
		{name: "timeout not retried", steps: []step{{stall: time.Second, status: 200}, ok}, wantHits: 1, wantErr: ErrUnreachable},
		{name: "bad JSON not retried", steps: []step{{status: 200, body: "<html>"}, ok}, wantHits: 1, wantErr: errDecode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			c := newClient(t, scripted(t, tt.steps, &hits))
			_, err := c.Info(t.Context())
			if !matches(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if got := hits.Load(); got != tt.wantHits {
				t.Errorf("hits = %d, want %d", got, tt.wantHits)
			}
		})
	}
}

var (
	errStatus = errors.New("status error")
	errDecode = errors.New("decode error")
)

func matches(err, want error) bool {
	switch {
	case want == nil:
		return err == nil
	case errors.Is(want, errStatus):
		_, ok := errors.AsType[*StatusError](err)
		return ok
	case errors.Is(want, errDecode):
		return err != nil && strings.Contains(err.Error(), "decode")
	default:
		return errors.Is(err, want)
	}
}

// TestExpiredTokenDetection: PocketBase lists an expired token as a guest
// (HTTP 200, no items); only Info reveals the 401.
func TestExpiredTokenDetection(t *testing.T) {
	tests := []struct {
		name       string
		systems    string
		infoStatus int
		wantErr    error
		wantInfo   int32
	}{
		{name: "empty list, info 401", systems: `{"items":[]}`, infoStatus: 401, wantErr: ErrUnauthorized, wantInfo: 1},
		{name: "empty list, info 403", systems: `{"items":[]}`, infoStatus: 403, wantErr: ErrUnauthorized, wantInfo: 1},
		{name: "empty list, info 200: really no systems", systems: `{"items":[]}`, infoStatus: 200, wantInfo: 1},
		{name: "systems present: info not called", systems: `{"items":[{"id":"a","name":"x"}]}`, infoStatus: 401, wantInfo: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var info atomic.Int32
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/api/beszel/info") {
					info.Add(1)
					w.WriteHeader(tt.infoStatus)
					_, _ = w.Write([]byte(`{"v":"0.21.1"}`))
					return
				}
				_, _ = w.Write([]byte(tt.systems))
			})
			_, err := newClient(t, h).Systems(t.Context())
			if !matches(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if got := info.Load(); got != tt.wantInfo {
				t.Errorf("info calls = %d, want %d", got, tt.wantInfo)
			}
		})
	}
}

func TestPaging(t *testing.T) {
	tests := []struct {
		name      string
		pages     []int // items per page
		wantItems int
		wantReqs  int32
	}{
		{name: "one short page", pages: []int{3}, wantItems: 3, wantReqs: 1},
		{name: "empty", pages: []int{0}, wantItems: 0, wantReqs: 1},
		{name: "full page then short", pages: []int{1000, 3}, wantItems: 1003, wantReqs: 2},
		{name: "exactly full then empty", pages: []int{1000, 0}, wantItems: 1000, wantReqs: 2},
		{name: "three pages", pages: []int{1000, 1000, 1}, wantItems: 2001, wantReqs: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var reqs atomic.Int32
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := int(reqs.Add(1))
				if r.URL.Query().Get("page") != strconv.Itoa(n) {
					t.Errorf("request %d asked for page %s", n, r.URL.Query().Get("page"))
				}
				items := make([]string, tt.pages[n-1])
				for i := range items {
					items[i] = `{"name":"c"}`
				}
				_, _ = fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
			})
			got, err := newClient(t, h).Containers(t.Context())
			if err != nil || len(got) != tt.wantItems || reqs.Load() != tt.wantReqs {
				t.Errorf("got %d items in %d requests (err %v), want %d in %d",
					len(got), reqs.Load(), err, tt.wantItems, tt.wantReqs)
			}
		})
	}
}

// TestRejectsUnsafeFilterInput: ids and types are checked before any request.
func TestRejectsUnsafeFilterInput(t *testing.T) {
	tests := []struct {
		name string
		id   string
		typ  StatType
	}{
		{name: "empty id", id: "", typ: Stat1m},
		{name: "upper case", id: "ABC", typ: Stat1m},
		{name: "quote injection", id: "x'||'1'='1", typ: Stat1m},
		{name: "space", id: "a b", typ: Stat1m},
		{name: "16 chars", id: "abcdefghijklmnop", typ: Stat1m},
		{name: "unknown type", id: bravoID, typ: "5m"},
		{name: "empty type", id: bravoID, typ: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var reqs atomic.Int32
			c := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reqs.Add(1)
				_, _ = w.Write([]byte(`{"items":[]}`))
			}))
			_, err1 := c.SystemStats(t.Context(), tt.id, tt.typ, since)
			_, err2 := c.ContainerStats(t.Context(), tt.id, tt.typ, since)
			_, err3 := c.MonitorStats(t.Context(), tt.id, tt.typ, since)
			if err1 == nil || err2 == nil || err3 == nil || reqs.Load() != 0 {
				t.Errorf("errs %v / %v / %v after %d requests; want 3 errors and no request", err1, err2, err3, reqs.Load())
			}
		})
	}
}

func TestMaxParallel(t *testing.T) {
	tests := []struct {
		name  string
		limit int32
	}{
		{name: "one at a time", limit: 1},
		{name: "three", limit: 3},
		{name: "eight", limit: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cur, peak atomic.Int32
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := cur.Add(1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				cur.Add(-1)
				_, _ = w.Write([]byte(`{"v":"0.21.1"}`))
			})
			c := newClient(t, h, func(o *Options) { o.MaxParallel = int(tt.limit) })
			var wg sync.WaitGroup
			for range 20 {
				wg.Go(func() {
					if _, err := c.Info(t.Context()); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if got := peak.Load(); got != tt.limit {
				t.Errorf("peak concurrency %d, want exactly %d", got, tt.limit)
			}
		})
	}
}

func TestTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		stall   time.Duration
		call    func(context.Context, *Client) error
		wantErr bool
	}{
		{
			name: "normal call within timeout", stall: 50 * time.Millisecond, wantErr: false,
			call: func(ctx context.Context, c *Client) error { _, err := c.Info(ctx); return err },
		},
		{
			name: "normal call past timeout", stall: 450 * time.Millisecond, wantErr: true,
			call: func(ctx context.Context, c *Client) error { _, err := c.Info(ctx); return err },
		},
		{
			name: "live call gets the longer timeout", stall: 450 * time.Millisecond, wantErr: false,
			call: func(ctx context.Context, c *Client) error { _, err := c.PackageUpdates(ctx, bravoID); return err },
		},
		{
			name: "live call past its timeout", stall: 900 * time.Millisecond, wantErr: true,
			call: func(ctx context.Context, c *Client) error { _, err := c.PackageUpdates(ctx, bravoID); return err },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hits atomic.Int32
			c := newClient(t, scripted(t, []step{{stall: tt.stall, status: 200, body: `{"v":"x","packages":[]}`}}, &hits))
			if err := tt.call(t.Context(), c); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestBodyLimit(t *testing.T) {
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "just under", size: maxBody - 64, wantErr: false},
		{name: "over", size: maxBody + 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				pad := tt.size - len(`{"v":""}`)
				_, _ = fmt.Fprintf(w, `{"v":"%s"}`, strings.Repeat("x", pad))
			})
			_, err := newClient(t, h, func(o *Options) { o.Timeout = 5 * time.Second }).Info(t.Context())
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func TestTimeParsing(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{name: "millis", in: `"2026-10-30 22:18:50.483Z"`, want: time.Date(2026, 10, 30, 22, 18, 50, 483e6, time.UTC)},
		{name: "no fraction", in: `"2026-10-30 22:18:50Z"`, want: time.Date(2026, 10, 30, 22, 18, 50, 0, time.UTC)},
		{name: "empty is zero", in: `""`, want: time.Time{}},
		{name: "rfc3339 rejected", in: `"2026-10-30T22:18:50Z"`, wantErr: true},
		{name: "number rejected", in: `1793355000001`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got Time
			err := json.Unmarshal([]byte(tt.in), &got)
			if (err != nil) != tt.wantErr || (!tt.wantErr && !got.Equal(tt.want)) {
				t.Errorf("got %v, err %v; want %v", got.Time, err, tt.want)
			}
		})
	}
}
