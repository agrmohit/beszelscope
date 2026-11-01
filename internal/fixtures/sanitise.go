// Package fixtures pseudonymises captured Beszel hub responses so they can
// be committed as test data.
package fixtures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Kind names the hub response a file holds and selects its rules.
type Kind string

// Response kinds the sanitiser knows.
const (
	KindInfo           Kind = "info"
	KindSystems        Kind = "systems"
	KindSystemDetails  Kind = "system_details"
	KindSystemStats    Kind = "system_stats"
	KindContainerStats Kind = "container_stats"
	KindContainers     Kind = "containers"
	KindServices       Kind = "systemd_services"
	KindSmart          Kind = "smart_devices"
	KindMonitors       Kind = "network_monitors"
	KindMonitorStats   Kind = "network_monitor_stats"
	KindPackageUpdates Kind = "package_updates"
)

// idKeys hold record IDs wherever they appear.
var idKeys = []string{"id", "system", "monitor", "user", "users", "alert_id"}

// Sanitiser maps each original value to one pseudonym across all files, so
// references between records survive.
type Sanitiser struct {
	mapped   map[string]string // category + "\x00" + original -> pseudonym
	next     map[string]int    // per-category counter
	replaced map[string]bool   // originals that were replaced, for Leaks
	kept     map[string]bool   // values deliberately kept somewhere; never a leak
	systems  map[string]string // original system id -> pseudonym name
	captured time.Time         // capture time; fake dates are set relative to it
}

// New returns an empty Sanitiser for a capture taken at capturedAt.
func New(capturedAt time.Time) *Sanitiser {
	return &Sanitiser{
		captured: capturedAt,
		mapped:   map[string]string{},
		next:     map[string]int{},
		replaced: map[string]bool{},
		kept:     map[string]bool{},
		systems:  map[string]string{},
	}
}

// File sanitises one captured response. Files that name systems must come
// after the systems file, so hostnames can follow their system's pseudonym.
func (s *Sanitiser) File(kind Kind, data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	// An object only: anything else would skip sanitising and be written raw.
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode %s: %w", kind, err)
	}
	if doc == nil {
		return nil, fmt.Errorf("decode %s: want a JSON object, got null", kind)
	}
	if kind == KindInfo {
		s.info(doc)
	} else {
		list, err := items(doc)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", kind, err)
		}
		for _, item := range list {
			s.record(kind, item)
		}
	}
	return encode(doc), nil
}

// encode writes compact JSON with one list item per line, so a re-capture
// diff shows which records changed.
func encode(obj map[string]any) []byte {
	list, isList := obj["items"].([]any)
	if !isList {
		return append(mustMarshal(obj), '\n')
	}
	envelope := maps.Clone(obj)
	delete(envelope, "items")
	head := mustMarshal(envelope)
	var buf bytes.Buffer
	buf.Write(head[:len(head)-1]) // drop the closing brace
	if len(envelope) > 0 {
		buf.WriteByte(',')
	}
	buf.WriteString(`"items":[`)
	for i, it := range list {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString("\n")
		buf.Write(mustMarshal(it))
	}
	buf.WriteString("\n]}\n")
	return buf.Bytes()
}

// mustMarshal encodes a value decoded from JSON (with UseNumber) and edited
// only with strings, so it cannot fail.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("fixtures: re-encoding decoded JSON failed: " + err.Error())
	}
	return b
}

// items returns the records of a list response, or the document itself.
func items(obj map[string]any) ([]map[string]any, error) {
	list, ok := obj["items"].([]any)
	if !ok {
		return []map[string]any{obj}, nil
	}
	out := make([]map[string]any, 0, len(list))
	for i, it := range list {
		m, ok := it.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("item %d is not an object", i)
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Sanitiser) info(obj map[string]any) {
	if key, ok := obj["key"].(string); ok && key != "" {
		s.replaced[key] = true
		obj["key"] = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFIXTUREFIXTUREFIXTUREFIXTUREFIXTUREFIXTUREFIX"
	}
}

func (s *Sanitiser) record(kind Kind, r map[string]any) {
	// Name the system before its id is replaced.
	if kind == KindSystems {
		if id, ok := r["id"].(string); ok {
			r["name"] = s.systemName(id, str(r["name"]))
		}
		r["host"] = s.host(str(r["host"]))
		s.wifi(r["info"])
	}
	if kind == KindSystemDetails {
		r["hostname"] = s.hostname(str(r["system"]), str(r["hostname"]))
	}
	for _, k := range idKeys {
		if v, ok := r[k]; ok {
			r[k] = s.ids(v)
		}
	}
	switch kind {
	case KindContainers:
		name := str(r["name"])
		r["name"] = s.container(name)
		r["image"] = s.image(name, str(r["image"]))
		// Host ports of a replaced container hint at the real service.
		if _, ok := r["ports"]; ok && !knownName(name) {
			r["ports"] = ""
		}
	case KindContainerStats:
		if stats, ok := r["stats"].([]any); ok {
			for _, st := range stats {
				if m, ok := st.(map[string]any); ok {
					m["n"] = s.container(str(m["n"]))
				}
			}
		}
	case KindServices:
		r["name"] = s.unit(str(r["name"]))
	case KindSmart:
		r["serial"] = s.fake("serial", str(r["serial"]))
	case KindMonitors:
		r["target"] = s.target(str(r["target"]))
		if srv := str(r["server"]); srv != "" {
			r["server"] = s.host(srv)
		}
		s.certExpiry(r["certInfo"])
	case KindInfo, KindSystems, KindSystemDetails, KindSystemStats, KindMonitorStats, KindPackageUpdates:
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// ids replaces a record ID or a list of them.
func (s *Sanitiser) ids(v any) any {
	switch t := v.(type) {
	case string:
		return s.fake("id", t)
	case []any:
		for i, e := range t {
			if id, ok := e.(string); ok {
				t[i] = s.fake("id", id)
			}
		}
		return t
	default:
		return v
	}
}

// remember returns the pseudonym for orig in cat, creating it with gen.
func (s *Sanitiser) remember(cat, orig string, gen func(n int) string) string {
	key := cat + "\x00" + orig
	if v, ok := s.mapped[key]; ok {
		return v
	}
	s.next[cat]++
	v := gen(s.next[cat])
	s.mapped[key] = v
	if v != orig {
		s.replaced[orig] = true
	}
	return v
}

// fake keeps the original's length. Hex digits fit every ID and serial
// alphabet seen in Beszel: [a-z0-9], [a-f0-9] and upper-case serials.
func (s *Sanitiser) fake(cat, orig string) string {
	if orig == "" {
		return ""
	}
	return s.remember(cat, orig, func(n int) string {
		h := strconv.FormatInt(int64(n), 16)
		if len(orig) <= len(h) {
			return h
		}
		v := "f" + strings.Repeat("0", len(orig)-1-len(h)) + h
		if strings.ToUpper(orig) == orig {
			v = strings.ToUpper(v)
		}
		return v
	})
}

var systemPool = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}

func (s *Sanitiser) systemName(id, orig string) string {
	name := s.remember("system", orig, func(n int) string { return pick(systemPool, n, "host") })
	s.systems[id] = name
	return name
}

// SystemName returns the pseudonym given to the system with the original id,
// or "" if the systems file has not been sanitised yet.
func (s *Sanitiser) SystemName(id string) string {
	return s.systems[id]
}

// hostname follows the pseudonym of its system, keeping a .local suffix.
func (s *Sanitiser) hostname(systemID, orig string) string {
	if orig == "" {
		return ""
	}
	name, ok := s.systems[systemID]
	if !ok {
		// Own prefix: system pseudonyms past the pool are host-N too.
		s.next["hostname"]++
		name = "orphan-" + strconv.Itoa(s.next["hostname"])
	}
	if strings.HasSuffix(orig, ".local") {
		name += ".local"
	}
	return s.remember("hostname", orig, func(int) string { return name })
}

// genericHosts identify nobody and stay as they are.
var genericHosts = []string{"localhost", "host.docker.internal", "127.0.0.1", "::1"}

// host maps addresses to TEST-NET-1 (RFC 5737) and names to example domains.
func (s *Sanitiser) host(orig string) string {
	if orig == "" || slices.Contains(genericHosts, orig) {
		return orig
	}
	if net.ParseIP(orig) != nil {
		return s.remember("ip", orig, func(n int) string { return "192.0.2." + strconv.Itoa(9+n) })
	}
	return s.remember("domain", orig, func(n int) string { return pick(domainPool, n, "example") })
}

var domainPool = []string{"example.com", "example.net", "example.org"}

// target rewrites a monitor target: a URL keeps its scheme and path.
func (s *Sanitiser) target(orig string) string {
	u, err := url.Parse(orig)
	if err != nil || u.Host == "" {
		return s.host(orig)
	}
	host := s.host(u.Hostname())
	if p := u.Port(); p != "" {
		host = net.JoinHostPort(host, p)
	}
	u.Host = host
	return u.String()
}

var ssidPool = []string{"home-5g", "home", "office"}

func (s *Sanitiser) wifi(info any) {
	m, _ := info.(map[string]any) // a nil map reads and ranges as empty
	wf, _ := m["wf"].(map[string]any)
	for _, v := range wf {
		if iface, ok := v.(map[string]any); ok {
			if ssid := str(iface["s"]); ssid != "" {
				iface["s"] = s.remember("ssid", ssid, func(n int) string { return pick(ssidPool, n, "wifi") })
			}
		}
	}
}

// certExpiry moves a real certificate expiry to 60 days after capture; the
// real date could identify the monitored site.
func (s *Sanitiser) certExpiry(cert any) {
	m, ok := cert.(map[string]any)
	if !ok {
		return
	}
	if exp, ok := m["expires"].(json.Number); ok {
		s.replaced[exp.String()] = true
		m["expires"] = json.Number(strconv.FormatInt(s.captured.Add(60*24*time.Hour).UnixMilli(), 10))
	}
}

func pick(pool []string, n int, prefix string) string {
	if n <= len(pool) {
		return pool[n-1]
	}
	return prefix + "-" + strconv.Itoa(n-len(pool))
}

// knownWords are the only container name tokens allowed in fixtures. A name
// stays only if every one of its tokens is here; everything else is replaced.
var knownWords = toSet(
	"adguard", "agent", "authelia", "beszel", "hub", "miniflux", "postgres", "readeck", "redis",
	"syncthing", "valkey",
)

var appPool = []struct{ name, image string }{
	{"gitea", "gitea/gitea:latest"},
	{"paperless", "ghcr.io/paperless-ngx/paperless-ngx:latest"},
	{"immich-server", "ghcr.io/immich-app/immich-server:release"},
	{"vaultwarden", "vaultwarden/server:latest"},
	{"nextcloud", "nextcloud:stable"},
	{"freshrss", "freshrss/freshrss:latest"},
	{"mealie", "ghcr.io/mealie-recipes/mealie:latest"},
	{"linkding", "sissbruecker/linkding:latest"},
	{"uptime-kuma", "louislam/uptime-kuma:1"},
	{"stirling-pdf", "stirlingtools/stirling-pdf:latest"},
	{"navidrome", "deluan/navidrome:latest"},
	{"audiobookshelf", "ghcr.io/advplyr/audiobookshelf:latest"},
	{"kavita", "jvmilazz0/kavita:latest"},
	{"gotify", "gotify/server:latest"},
	{"ntfy", "binwiederhier/ntfy:latest"},
	{"changedetection", "ghcr.io/dgtlmoon/changedetection.io:latest"},
	{"wallabag", "wallabag/wallabag:latest"},
	{"photoprism", "photoprism/photoprism:latest"},
	{"home-assistant", "ghcr.io/home-assistant/home-assistant:stable"},
	{"nocodb", "nocodb/nocodb:latest"},
	{"n8n", "n8nio/n8n:latest"},
	{"actual", "actualbudget/actual-server:latest"},
	{"memos", "neosmartpen/memos:stable"},
	{"karakeep", "ghcr.io/karakeep-app/karakeep:release"},
}

func (s *Sanitiser) container(orig string) string {
	if orig == "" || knownName(orig) {
		return orig
	}
	return s.remember("container", orig, func(n int) string {
		if n <= len(appPool) {
			return appPool[n-1].name
		}
		return "app-" + strconv.Itoa(n-len(appPool))
	})
}

// image keeps a public image of a kept container; anything else follows the
// container's stand-in.
func (s *Sanitiser) image(name, orig string) string {
	if knownName(name) && !strings.HasPrefix(orig, "sha256:") && !strings.HasSuffix(orig, ":local") {
		s.kept[orig] = true
		return orig
	}
	if knownName(name) {
		return s.remember("digest", orig, func(n int) string {
			return "sha256:" + fmt.Sprintf("%064x", n)
		})
	}
	stand := s.container(name)
	for _, a := range appPool {
		if a.name == stand {
			s.replaced[orig] = true
			return a.image
		}
	}
	s.replaced[orig] = true
	return "example/" + stand + ":latest"
}

func knownName(name string) bool {
	for _, tok := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' }) {
		if _, err := strconv.Atoi(tok); err == nil {
			continue
		}
		if !knownWords[tok] {
			return false
		}
	}
	return name != ""
}

// knownUnits are common distro systemd units; prefixes end in '-'.
var knownUnits = []string{
	"systemd-", "dracut-", "cloud-", "NetworkManager", "lvm2-", "kmod-", "getty", "serial-getty",
	"user", "user-runtime-dir", "sshd", "chronyd", "auditd", "dbus-broker", "docker", "containerd",
	"avahi-daemon", "AdGuardHome", "syncthing",
}

// genericInstance matches unit instances that identify nobody.
var genericInstance = regexp.MustCompile(`^(tty\w*|\d+|zram\d+|dev-.*)$`)

var unitPool = []string{"restic-backup", "fail2ban", "caddy", "borgmatic", "smartd", "cockpit", "crowdsec", "node-exporter"}

func (s *Sanitiser) unit(orig string) string {
	base, inst, hasInst := strings.Cut(orig, "@")
	if !knownUnit(base) {
		return s.remember("unit", orig, func(n int) string { return pick(unitPool, n, "unit") })
	}
	if uuid, ok := strings.CutPrefix(inst, "dev-disk-by-uuid-"); ok {
		s.replaced[uuid] = true
		return base + "@dev-disk-by-uuid-0000-0000"
	}
	if hasInst && !genericInstance.MatchString(inst) {
		s.replaced[inst] = true
		return base + "@user"
	}
	return orig
}

func knownUnit(base string) bool {
	for _, k := range knownUnits {
		if base == k || strings.HasSuffix(k, "-") && strings.HasPrefix(base, k) || strings.HasPrefix(base, k+"-") {
			return true
		}
	}
	return false
}

func toSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// denyPatterns flag anything that looks private, mapped or not.
var denyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(10\.\d{1,3}|172\.(1[6-9]|2\d|3[01])|192\.168|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7]))\.\d{1,3}\.\d{1,3}\b`),
	regexp.MustCompile(`(?i)\b([0-9a-f]{2}:){5}[0-9a-f]{2}\b`),
	regexp.MustCompile(`[\w.+-]+@[\w-]+\.[a-z]{2,}`),
}

// Leaks reports every replaced original, deny word (case-insensitive) or
// private-looking value still present in files (name -> content).
// Originals shorter than 4 chars are skipped, and kept values are masked
// first, so "postgres:18" inside a kept "postgres:18-alpine" is not a leak.
func (s *Sanitiser) Leaks(files map[string][]byte, denyWords []string) []string {
	// Longest first, so a kept value never masks only part of a longer one.
	kept := slices.SortedFunc(maps.Keys(s.kept), func(a, b string) int { return len(b) - len(a) })
	var out []string
	for _, name := range slices.Sorted(maps.Keys(files)) {
		body := string(files[name])
		masked := body
		for _, k := range kept {
			masked = strings.ReplaceAll(masked, k, "\x00")
		}
		for orig := range s.replaced {
			if len(orig) >= 4 && !s.kept[orig] && strings.Contains(masked, orig) {
				out = append(out, name+": still contains "+strconv.Quote(orig))
			}
		}
		lower := strings.ToLower(body)
		for _, w := range denyWords {
			if w != "" && strings.Contains(lower, strings.ToLower(w)) {
				out = append(out, name+": contains deny word "+strconv.Quote(w))
			}
		}
		for _, p := range denyPatterns {
			if m := p.FindString(body); m != "" {
				out = append(out, name+": matches "+p.String()+": "+strconv.Quote(m))
			}
		}
	}
	slices.Sort(out)
	return out
}
