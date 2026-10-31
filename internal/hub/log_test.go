package hub

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestLogTransport(t *testing.T) {
	ok := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	status := func(code int) http.RoundTripper {
		return roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: code, Body: http.NoBody}, nil
		})
	}
	broken := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection reset") })
	tests := []struct {
		name   string
		next   http.RoundTripper
		url    string
		want   []string
		absent []string
	}{
		{
			name: "200 at info, query decoded", next: ok,
			url:  "https://hub.example.com/api/collections/system_stats/records?filter=system%3D%27abc%27&perPage=1000",
			want: []string{`"level":"INFO"`, `"msg":"hub.request"`, `"http.request.method":"GET"`, `"url.path":"/api/collections/system_stats/records"`, `"url.query":"filter=system='abc'&perPage=1000"`, `"http.response.status_code":200`, `"duration_ms":`},
		},
		{name: "no query", next: ok, url: "https://hub.example.com/api/beszel/info", want: []string{`"url.query":""`, `"http.response.status_code":200`}},
		{name: "401 at warn", next: status(401), url: "https://hub.example.com/api/beszel/info", want: []string{`"level":"WARN"`, `"http.response.status_code":401`}},
		{name: "503 at warn", next: status(503), url: "https://hub.example.com/x", want: []string{`"level":"WARN"`, `"http.response.status_code":503`}},
		{name: "201 is not a 200", next: status(201), url: "https://hub.example.com/x", want: []string{`"level":"WARN"`, `"http.response.status_code":201`}},
		{name: "transport error at warn, no status", next: broken, url: "https://hub.example.com/x", want: []string{`"level":"WARN"`, `"error":"connection reset"`}, absent: []string{`"http.response.status_code"`}},
		{name: "bad escape kept raw", next: ok, url: "https://hub.example.com/x?a=%zz", want: []string{`"url.query":"a=%zz"`}},
		{name: "the token never appears", next: ok, url: "https://hub.example.com/x", absent: []string{"secret-token", "Authorization"}},
		{
			name: "a long error is cut at 512 runes and marked", url: "https://hub.example.com/x",
			next: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(strings.Repeat("é", 600)) }),
			want: []string{`"error":"` + strings.Repeat("é", 512) + `...(truncated)"`},
		},
		{
			name: "an error of exactly 512 runes is kept whole", url: "https://hub.example.com/x",
			next:   roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(strings.Repeat("é", 512)) }),
			want:   []string{`"error":"` + strings.Repeat("é", 512) + `"`},
			absent: []string{"truncated"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			rt := LogTransport(tt.next, slog.New(slog.NewJSONHandler(&buf, nil)))
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tt.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "secret-token")
			resp, err := rt.RoundTrip(req)
			if err == nil {
				_ = resp.Body.Close()
			}
			got := buf.String()
			if strings.Count(got, "\n") != 1 {
				t.Fatalf("want exactly one line, got %q", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s in %s", w, got)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %s in %s", a, got)
				}
			}
		})
	}
}
