package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agrmohit/beszelscope/internal/fixtures"
)

var statTypes = []string{"1m", "10m", "20m", "120m", "480m"}

// rawFile is one captured response. In file, "{sys}" becomes the system's pseudonym.
type rawFile struct {
	kind fixtures.Kind
	file string
	sys  string // original system id for {sys}
	body []byte
}

func (r rawFile) fileName(s *fixtures.Sanitiser) string {
	return strings.ReplaceAll(r.file, "{sys}", s.SystemName(r.sys))
}

type meta struct {
	CapturedAt          time.Time `json:"captured_at"`
	HubVersion          string    `json:"hub_version"`
	FullSystem          string    `json:"full_system"`
	PackageUpdatesError string    `json:"package_updates_error,omitempty"`
}

type client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func (c *client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := c.base.JoinPath(path)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d: %.200s", path, resp.StatusCode, body)
	}
	return body, nil
}

type page struct {
	Page       int               `json:"page"`
	PerPage    int               `json:"perPage"`
	TotalItems int               `json:"totalItems"`
	TotalPages int               `json:"totalPages"`
	Items      []json.RawMessage `json:"items"`
}

// list fetches every page of a collection and returns them as one page.
func (c *client) list(ctx context.Context, collection string, q url.Values) ([]byte, error) {
	var all page
	for n := 1; ; n++ {
		q.Set("perPage", "1000")
		q.Set("page", strconv.Itoa(n))
		body, err := c.get(ctx, "api/collections/"+collection+"/records", q)
		if err != nil {
			return nil, err
		}
		var p page
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, fmt.Errorf("decode %s: %w", collection, err)
		}
		all.Items = append(all.Items, p.Items...)
		if n >= p.TotalPages {
			break
		}
	}
	all.Page, all.PerPage, all.TotalItems, all.TotalPages = 1, len(all.Items), len(all.Items), 1
	b, err := json.Marshal(all)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", collection, err)
	}
	return b, nil
}

var recordID = regexp.MustCompile(`^[a-z0-9]+$`)

func filter(systemID, statType string) (string, error) {
	if !recordID.MatchString(systemID) {
		return "", fmt.Errorf("unexpected system id %q", systemID)
	}
	return fmt.Sprintf("system='%s' && type='%s'", systemID, statType), nil
}

// capture fetches everything the tests need; systems come first so later
// files can be named after system pseudonyms.
func capture(ctx context.Context, c *client) ([]rawFile, meta, error) {
	m := meta{CapturedAt: time.Now().UTC().Truncate(time.Second)}
	var files []rawFile
	add := func(kind fixtures.Kind, file, sys string, body []byte) {
		files = append(files, rawFile{kind, file, sys, body})
	}

	info, err := c.get(ctx, "api/beszel/info", nil)
	if err != nil {
		return nil, m, err
	}
	var hub struct {
		V string `json:"v"`
	}
	if err := json.Unmarshal(info, &hub); err != nil {
		return nil, m, fmt.Errorf("decode info: %w", err)
	}
	m.HubVersion = hub.V
	add(fixtures.KindInfo, "info.json", "", info)

	collections := []struct {
		kind fixtures.Kind
		sort string
	}{
		{fixtures.KindSystems, "name"},
		{fixtures.KindSystemDetails, "system"},
		{fixtures.KindContainers, "system,name"},
		{fixtures.KindServices, "system,name"},
		{fixtures.KindSmart, "system,name"},
		{fixtures.KindMonitors, "system,target"},
	}
	bodies := map[fixtures.Kind][]byte{}
	for _, col := range collections {
		body, err := c.list(ctx, string(col.kind), url.Values{"sort": {col.sort}})
		if err != nil {
			return nil, m, err
		}
		bodies[col.kind] = body
		add(col.kind, string(col.kind)+".json", "", body)
	}

	systems, full, err := pickSystems(bodies[fixtures.KindSystems], bodies[fixtures.KindContainers])
	if err != nil {
		return nil, m, err
	}
	m.FullSystem = full

	for _, id := range systems {
		f, err := filter(id, "1m")
		if err != nil {
			return nil, m, err
		}
		for _, kind := range []fixtures.Kind{fixtures.KindSystemStats, fixtures.KindContainerStats} {
			q := url.Values{"filter": {f}, "sort": {"-created"}, "perPage": {"1"}}
			body, err := c.get(ctx, "api/collections/"+string(kind)+"/records", q)
			if err != nil {
				return nil, m, err
			}
			add(kind, string(kind)+"_latest_{sys}.json", id, body)
		}
	}

	for _, t := range statTypes {
		f, err := filter(full, t)
		if err != nil {
			return nil, m, err
		}
		for _, kind := range []fixtures.Kind{fixtures.KindSystemStats, fixtures.KindContainerStats, fixtures.KindMonitorStats} {
			// Container history is large; the 1h and 24h windows are enough.
			if kind == fixtures.KindContainerStats && t != "1m" && t != "20m" {
				continue
			}
			body, err := c.list(ctx, string(kind), url.Values{"filter": {f}, "sort": {"created"}})
			if err != nil {
				return nil, m, err
			}
			add(kind, string(kind)+"_{sys}_"+t+".json", full, body)
		}
	}

	body, err := c.get(ctx, "api/beszel/package-updates", url.Values{"system": {full}})
	if err != nil {
		m.PackageUpdatesError = err.Error()
	} else {
		add(fixtures.KindPackageUpdates, "package_updates_{sys}.json", full, body)
	}
	return files, m, nil
}

// pickSystems returns every system id and the one to capture in full: the
// up system with the most containers.
func pickSystems(systemsBody, containersBody []byte) (ids []string, full string, err error) {
	var systems struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	var containers struct {
		Items []struct {
			System string `json:"system"`
		} `json:"items"`
	}
	if err := json.Unmarshal(systemsBody, &systems); err != nil {
		return nil, "", fmt.Errorf("decode systems: %w", err)
	}
	if err := json.Unmarshal(containersBody, &containers); err != nil {
		return nil, "", fmt.Errorf("decode containers: %w", err)
	}
	count := map[string]int{}
	for _, c := range containers.Items {
		count[c.System]++
	}
	best := -1
	for _, s := range systems.Items {
		ids = append(ids, s.ID)
		if s.Status == "up" && count[s.ID] > best {
			full, best = s.ID, count[s.ID]
		}
	}
	if full == "" {
		return nil, "", errors.New("no system is up")
	}
	return ids, full, nil
}
