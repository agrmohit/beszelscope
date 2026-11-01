package mcptools

import (
	"context"
	"slices"
	"time"

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
	System       string           `json:"system" jsonschema:"The server, by name or ID. Names are case-insensitive and a unique prefix works; an unknown or ambiguous name returns an error listing the valid ones."`
	Sections     []report.Section `json:"sections,omitempty" jsonschema:"Sections to return; omit for summary only."`
	Filter       string           `json:"filter,omitempty" jsonschema:"Keep containers whose name or image, and services whose name, contains this text, ignoring case."`
	ProblemsOnly bool             `json:"problems_only,omitempty" jsonschema:"Keep only unhealthy containers and failed services."`
	Sort         report.SortKey   `json:"sort,omitempty" jsonschema:"Order of containers and services after problem rows: cpu (highest first; when omitted), mem (highest first) or name."`
	Limit        int              `json:"limit,omitempty" jsonschema:"Most containers and services rows to return besides problem rows; 25 when omitted."`
}

// latestWithin bounds how old the newest stats sample may be. Agents report
// every 60 s, so 5 minutes allows a few missed polls.
const latestWithin = 5 * time.Minute

func addDetail(s *mcp.Server, t *tools) {
	in := inputSchema[detailIn]("system_detail")
	sections := in.Properties["sections"]
	array(sections, 0, len(report.Sections))
	sections.Items.Enum = enum(report.Sections)
	in.Properties["sort"].Enum = enum(report.Sorts)
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
	picked, ask, err := resolveAll(req, systems, []string{in.System})
	if ask != nil || err != nil {
		return ask, report.DetailResult{}, err
	}
	sys := picked[0]

	input := report.DetailInput{
		FleetInput: report.FleetInput{
			Now: now, HubVersion: t.d.HubVersion, TokenExpiresAt: t.d.TokenExpiresAt,
			RecentContainers: map[string]report.Read[[]hub.ContainerStats]{}, LastSeen: map[string]report.Read[time.Time]{},
		},
		System: sys,
	}
	want := func(section report.Section) bool {
		return len(in.Sections) == 0 && section == report.SectionSummary || slices.Contains(in.Sections, section)
	}
	// Each source is read once, and only for the sections that use it.
	var fe fetcher
	up := sys.Status == hub.StatusUp
	if !up {
		t.readLastSeen(ctx, &fe, &input.FleetInput, sys)
	}
	if want(report.SectionSummary) || want(report.SectionUpdates) && up {
		read(ctx, &fe, &input.Details, func(ctx context.Context) (*hub.SystemDetails, error) {
			all, err := t.d.Hub.SystemDetails(ctx)
			if i := slices.IndexFunc(all, func(d hub.SystemDetails) bool { return d.System == sys.ID }); i >= 0 {
				return &all[i], err
			}
			return nil, err
		})
	}
	if want(report.SectionSummary) || want(report.SectionServices) && up {
		read(ctx, &fe, &input.Services, t.d.Hub.Services)
	}
	if want(report.SectionSummary) || want(report.SectionContainers) && up {
		read(ctx, &fe, &input.Containers, t.d.Hub.Containers)
		if up {
			readKey(ctx, &fe, input.RecentContainers, sys.ID, func(ctx context.Context) ([]hub.ContainerStats, error) {
				return t.d.Hub.ContainerStats(ctx, sys.ID, hub.Stat10m, now.Add(-time.Hour))
			})
		}
	}
	if want(report.SectionSummary) || want(report.SectionNetwork) {
		read(ctx, &fe, &input.Monitors, t.d.Hub.Monitors)
	}
	if want(report.SectionSummary) || want(report.SectionDisk) {
		read(ctx, &fe, &input.Smart, t.d.Hub.SmartDevices)
		read(ctx, &fe, &input.Pools, t.d.Hub.ZfsPools)
	}
	if (want(report.SectionCore) || want(report.SectionNetwork) || want(report.SectionDisk) || want(report.SectionGPU)) && up {
		read(ctx, &fe, &input.Latest, func(ctx context.Context) (*hub.SystemStats, error) {
			rows, err := t.d.Hub.SystemStats(ctx, sys.ID, hub.Stat1m, now.Add(-latestWithin))
			if err != nil || len(rows) == 0 {
				return nil, err
			}
			newest := slices.MaxFunc(rows, func(a, b hub.SystemStats) int { return a.Created.Compare(b.Created.Time) })
			return &newest, nil
		})
	}
	if want(report.SectionUpdates) && up {
		read(ctx, &fe, &input.Packages, func(ctx context.Context) (*hub.PackageUpdates, error) {
			p, err := t.d.Hub.PackageUpdates(ctx, sys.ID)
			if err != nil {
				return nil, err
			}
			return &p, nil
		})
	}
	fe.wait()

	res, err := report.Detail(input, report.DetailOptions{
		Sections: in.Sections, Filter: in.Filter, ProblemsOnly: in.ProblemsOnly, Sort: in.Sort, Limit: in.Limit,
	})
	if err != nil {
		return nil, report.DetailResult{}, err
	}
	return nil, res, nil
}
