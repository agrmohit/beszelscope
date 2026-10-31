package mcptools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agrmohit/beszelscope/internal/hub"
	"github.com/agrmohit/beszelscope/internal/report"
)

// Sent to the model as the tool description; rewording changes behaviour.
const detailDescription = "Current detail for one server monitored by the Beszel hub. " +
	"Pick the server by name or ID, and pick sections; without sections you get summary. " +
	"summary has OS, kernel, hostname, CPU model and core count, total memory, uptime, " +
	"the headline numbers (CPU, memory, fullest disk, load average and load per CPU thread, network, GPU, battery) " +
	"and this server's problems. " +
	"core has CPU use in total, by state (user, system, iowait, steal, ...) and per core, " +
	"load average, memory, swap, every temperature and fan sensor (on a Mac, the PMU tdie sensors are the CPU dies), and battery. " +
	"network has traffic per interface in bytes per second and in total, WiFi signal, and network monitors: " +
	"probes from this server to a target, with latency, packet loss in the last hour and certificate days left. " +
	"disk has each filesystem's size in GiB, use and I/O, ZFS and btrfs pools, and SMART health per drive; " +
	"for a down server it still shows the last SMART and pool results. " +
	"gpu has each GPU's use, memory and power. " +
	"containers has running containers with health, CPU, memory and network; Beszel drops a stopped container " +
	"within 10 minutes, and containers that stopped in the last hour are listed in gone. " +
	"services has systemd units with state, CPU and memory. " +
	"updates has pending package updates with versions, security updates first; " +
	"Beszel checks packages only on agents running natively on Linux. " +
	"filter, problems_only, sort and limit shape the containers and services lists only; " +
	"problem rows (unhealthy containers, failed units) always come first and are never cut, and hints say when rows were left out. " +
	"Values are the latest 60 s poll; for trends use metric_history."

type detailIn struct {
	System       string   `json:"system" jsonschema:"The server, by name or ID. Names are case-insensitive and a unique prefix works; an unknown or ambiguous name returns an error listing the valid ones."`
	Sections     []string `json:"sections,omitempty" jsonschema:"Sections to return; omit for summary only."`
	Filter       string   `json:"filter,omitempty" jsonschema:"Keep containers whose name or image, and services whose name, contains this text, ignoring case."`
	ProblemsOnly bool     `json:"problems_only,omitempty" jsonschema:"Keep only unhealthy containers and failed services."`
	Sort         string   `json:"sort,omitempty" jsonschema:"Order of containers and services after problem rows: cpu (highest first; when omitted), mem (highest first) or name."`
	Limit        int      `json:"limit,omitempty" jsonschema:"Most containers and services rows to return besides problem rows; 25 when omitted."`
}

// latestWithin bounds how old the newest stats sample may be. Agents report
// every 60 s, so 5 minutes allows a few missed polls.
const latestWithin = 5 * time.Minute

func addDetail(s *mcp.Server, t *tools) {
	in, err := jsonschema.For[detailIn](nil)
	if err != nil {
		panic(fmt.Sprintf("system_detail input schema: %v", err)) // fixed type; fails only on a code bug
	}
	sections := in.Properties["sections"]
	sections.Items.Enum = make([]any, len(report.Sections))
	for i, name := range report.Sections {
		sections.Items.Enum[i] = name
	}
	sections.Types, sections.Type = nil, "array" // not ["null","array"]
	sections.UniqueItems = true
	sections.MaxItems = new(len(report.Sections))
	sort := in.Properties["sort"]
	sort.Enum = make([]any, len(report.Sorts))
	for i, name := range report.Sorts {
		sort.Enum[i] = name
	}
	limit := in.Properties["limit"]
	limit.Minimum, limit.Maximum = new(1.0), new(float64(report.MaxLimit))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "system_detail",
		Description: detailDescription,
		InputSchema: in,
		Annotations: readOnly,
	}, instrument(t, "system_detail", t.detail))
}

func (t *tools) detail(ctx context.Context, req *mcp.CallToolRequest, in detailIn) (*mcp.CallToolResult, report.DetailResult, error) {
	now := t.d.Now()
	systems, ask, err := t.systems(ctx, req)
	if ask != nil || err != nil {
		return ask, report.DetailResult{}, err
	}
	ids, ask, err := resolveAll(req, systems, []string{in.System})
	if ask != nil || err != nil {
		return ask, report.DetailResult{}, err
	}
	sys := systems[slices.IndexFunc(systems, func(s hub.System) bool { return s.ID == ids[0] })]

	input := report.DetailInput{
		FleetInput: report.FleetInput{
			Now: now, HubVersion: t.d.HubVersion, TokenExpiresAt: t.d.TokenExpiresAt, Systems: systems,
			LastSeen: map[string]time.Time{},
		},
		System: sys,
	}
	want := func(section string) bool {
		return len(in.Sections) == 0 && section == report.SectionSummary || slices.Contains(in.Sections, section)
	}
	// A list section reports its own failed read; only the summary's
	// problems need a warning.
	summaryErr := func(err error) error {
		if want(report.SectionSummary) {
			return err
		}
		return nil
	}
	// Each source is read once, and only for the sections that use it.
	var fe fetcher
	up := sys.Status == "up"
	if !up {
		t.lastSeen(ctx, &fe, sys, &input.FleetInput)
	}
	if want(report.SectionSummary) || want(report.SectionUpdates) && up {
		fe.run("system details", "the summary lacks OS and CPU facts", func() error {
			all, err := t.d.Hub.SystemDetails(ctx)
			for i := range all {
				if all[i].System == sys.ID {
					input.Details = &all[i]
				}
			}
			return err
		})
	}
	if want(report.SectionSummary) || want(report.SectionServices) && up {
		fe.run("systemd services", "related problems may be missing", func() (err error) {
			input.Services, err = t.d.Hub.Services(ctx)
			input.ServicesErr = err
			return summaryErr(err)
		})
	}
	if want(report.SectionSummary) || want(report.SectionContainers) && up {
		fe.run("containers", "related problems may be missing", func() (err error) {
			input.Containers, err = t.d.Hub.Containers(ctx)
			input.ContainersErr = err
			return summaryErr(err)
		})
		if up {
			fe.run("container history", "stopped containers may be missing", func() (err error) {
				input.RecentContainers, err = t.d.Hub.ContainerStats(ctx, sys.ID, hub.Stat10m, now.Add(-time.Hour))
				return err
			})
		}
	}
	const lacks = "its rows and related problems may be missing"
	if want(report.SectionSummary) || want(report.SectionNetwork) {
		fe.run("network monitors", lacks, func() (err error) { input.Monitors, err = t.d.Hub.Monitors(ctx); return err })
	}
	if want(report.SectionSummary) || want(report.SectionDisk) {
		fe.run("SMART devices", lacks, func() (err error) { input.Smart, err = t.d.Hub.SmartDevices(ctx); return err })
		fe.run("storage pools", lacks, func() (err error) { input.Pools, err = t.d.Hub.ZfsPools(ctx); return err })
	}
	if (want(report.SectionCore) || want(report.SectionNetwork) || want(report.SectionDisk) || want(report.SectionGPU)) && up {
		fe.run("stats", "the stats sections are missing", func() error {
			rows, err := t.d.Hub.SystemStats(ctx, sys.ID, hub.Stat1m, now.Add(-latestWithin))
			if err != nil {
				input.StatsErr = err
				return nil // the sections report it themselves
			}
			for i := range rows {
				if input.Latest == nil || rows[i].Created.After(input.Latest.Created.Time) {
					input.Latest = &rows[i]
				}
			}
			return nil
		})
	}
	if want(report.SectionUpdates) && up {
		fe.run("package updates", "", func() error {
			p, err := t.d.Hub.PackageUpdates(ctx, sys.ID)
			if err != nil {
				input.PackagesErr = err
				return nil // the updates section reports it
			}
			input.Packages = &p
			return nil
		})
	}
	warnings := fe.wait()

	res, err := report.Detail(input, report.DetailOptions{
		Sections: in.Sections, Filter: in.Filter, ProblemsOnly: in.ProblemsOnly, Sort: in.Sort, Limit: in.Limit,
	})
	if err != nil {
		return nil, report.DetailResult{}, err
	}
	res.Warnings = append(res.Warnings, warnings...)
	return nil, res, nil
}

// systems lists every system. A rejected token offers renewal once, when
// the client can open a URL.
func (t *tools) systems(ctx context.Context, req *mcp.CallToolRequest) ([]hub.System, *mcp.CallToolResult, error) {
	systems, err := t.d.Hub.Systems(ctx)
	if err == nil {
		return systems, nil, nil
	}
	if errors.Is(err, hub.ErrUnauthorized) {
		if ask := t.renewalRequest(req); ask != nil {
			return nil, ask, nil
		}
	}
	return nil, nil, hubError(err)
}
