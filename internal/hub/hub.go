// Package hub is a GET-only client for a Beszel hub's PocketBase API. Its
// method set is the allowlist: there is no generic request method.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var (
	// ErrUnauthorized means the hub rejected the token: expired, revoked or wrong.
	ErrUnauthorized = errors.New("hub rejected the token")
	// ErrUnreachable means the request never got an HTTP response.
	ErrUnreachable = errors.New("hub unreachable")
)

// StatusError is an unexpected HTTP status from the hub.
type StatusError struct {
	Code int
	Body string // first 512 bytes
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("hub returned HTTP %d: %s", e.Code, e.Body)
}

const (
	maxBody = 16 << 20
	perPage = 1000 // PocketBase's maximum
)

// Options configures a Client. Zero durations and MaxParallel are invalid.
type Options struct {
	BaseURL     *url.URL
	Token       string
	HTTP        *http.Client
	Timeout     time.Duration // per request
	LiveTimeout time.Duration // per request that waits on an agent
	RetryDelay  time.Duration
	MaxParallel int // across all callers
}

// Client reads from one hub. It is safe for concurrent use.
type Client struct {
	o   Options
	sem chan struct{}
}

// New returns a Client.
func New(o Options) *Client {
	return &Client{o: o, sem: make(chan struct{}, o.MaxParallel)}
}

// NewTransport returns the transport for hub requests: HTTP/2, keep-alive
// and no proxy from the environment.
func NewTransport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 8, // every request goes to one host; the default keeps 2
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
	}
}

// get fetches path (relative to the base URL) into out, retrying once on a
// quick failure.
func (c *Client) get(ctx context.Context, path string, q url.Values, timeout time.Duration, out any) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return fmt.Errorf("wait for a free slot: %w", ctx.Err())
	}

	err := c.attempt(ctx, path, q, timeout, out)
	if !retryable(ctx, err) {
		return err
	}
	select {
	case <-time.After(c.o.RetryDelay):
	case <-ctx.Done():
		return fmt.Errorf("retry %s: %w", path, ctx.Err())
	}
	return c.attempt(ctx, path, q, timeout, out)
}

func (c *Client) attempt(ctx context.Context, path string, q url.Values, timeout time.Duration, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	u := c.o.BaseURL.JoinPath(path)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", c.o.Token)

	resp, err := c.o.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: GET %s: %w", ErrUnreachable, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: GET %s: HTTP %d", ErrUnauthorized, path, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET %s: %w", path, &StatusError{Code: resp.StatusCode, Body: string(body)})
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("%w: read %s: %w", ErrUnreachable, path, err)
	}
	if len(body) > maxBody {
		return fmt.Errorf("GET %s: response over %d bytes", path, maxBody)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// retryable reports whether err is a quick failure worth one retry:
// a connection error or 502/503/504. Timeouts and cancellation are not.
func retryable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.Code == http.StatusBadGateway || se.Code == http.StatusServiceUnavailable ||
			se.Code == http.StatusGatewayTimeout
	}
	if !errors.Is(err, ErrUnreachable) {
		return false
	}
	// A timeout means the hub or agent is slow; a retry only doubles the wait.
	ne, ok := errors.AsType[net.Error](err)
	return !ok || !ne.Timeout()
}

type page[T any] struct {
	Items []T `json:"items"`
}

// list reads every record of a collection, 1000 per request.
func list[T any](ctx context.Context, c *Client, collection, fields, filter, sort string) ([]T, error) {
	q := url.Values{
		"fields":    {fields},
		"perPage":   {strconv.Itoa(perPage)},
		"skipTotal": {"true"},
	}
	if filter != "" {
		q.Set("filter", filter)
	}
	if sort != "" {
		q.Set("sort", sort)
	}
	var all []T
	for n := 1; ; n++ {
		q.Set("page", strconv.Itoa(n))
		var p page[T]
		if err := c.get(ctx, "api/collections/"+collection+"/records", q, c.o.Timeout, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if len(p.Items) < perPage {
			return all, nil
		}
	}
}
