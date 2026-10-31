package hub

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// LogTransport logs every request it carries as hub.request: info for a 200,
// warn for anything else. Wrap it inside otelhttp, so each line carries the
// request span's trace_id and span_id. Field names are the OTel semantic
// convention HTTP keys. Headers are never logged: they hold the token.
// duration_ms runs until the response headers arrive.
func LogTransport(next http.RoundTripper, log *slog.Logger) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		start := time.Now()
		resp, err := next.RoundTrip(r)
		query, qerr := url.QueryUnescape(r.URL.RawQuery)
		if qerr != nil {
			query = r.URL.RawQuery
		}
		attrs := []slog.Attr{
			slog.String(string(semconv.HTTPRequestMethodKey), r.Method), slog.String(string(semconv.URLPathKey), r.URL.Path),
			slog.String(string(semconv.URLQueryKey), query),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		}
		level := slog.LevelInfo
		switch {
		case err != nil:
			level = slog.LevelWarn
			attrs = append(attrs, slog.String("error", trunc(err.Error(), maxLoggedError)))
		case resp.StatusCode != http.StatusOK:
			level = slog.LevelWarn
			attrs = append(attrs, slog.Int(string(semconv.HTTPResponseStatusCodeKey), resp.StatusCode))
		default:
			attrs = append(attrs, slog.Int(string(semconv.HTTPResponseStatusCodeKey), resp.StatusCode))
		}
		log.LogAttrs(r.Context(), level, "hub.request", attrs...)
		return resp, err //nolint:wrapcheck // a transport passes its inner transport's error through
	})
}

// maxLoggedError caps a logged error, in runes. Errors carry upstream text,
// so their length is not ours to bound.
const maxLoggedError = 512

// trunc cuts s to n runes and marks the cut, so a shortened value never
// passes for a whole one.
func trunc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "...(truncated)"
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
