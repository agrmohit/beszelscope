package mcptools

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agrmohit/beszelscope/internal/hub"
	"github.com/agrmohit/beszelscope/internal/report"
)

// Sent to the model as the tool description; rewording changes behaviour.
const fleetDescription = "Health of every server monitored by the Beszel hub, one compact row per server. " +
	"Each row has status, CPU %, memory %, the fullest disk %, the hottest temperature sensor, load per CPU thread, " +
	"network bytes per second, uptime, agent version and how old the data is. " +
	"It also lists problems: down, paused or pending servers, stale data, failed systemd units, " +
	"unhealthy or stopped containers, SMART and storage-pool faults, packet loss on network monitors, " +
	"expiring certificates and pending security updates. Each problem names the system_detail section that explains it. " +
	`Use it for questions about the whole fleet, like "how are my servers" or "is anything wrong"; ` +
	"for one named server, call system_detail directly. " +
	"Values are the latest 60 s poll; for trends use metric_history."

type fleetIn struct {
	Systems      []string `json:"systems,omitempty" jsonschema:"Servers to include, by name or ID; omit for all. Names are case-insensitive and a unique prefix works; an unknown or ambiguous name returns an error listing the valid ones."`
	ProblemsOnly bool     `json:"problems_only,omitempty" jsonschema:"Return only servers with problems. The counts block (total, up, down, with problems) still covers every server."`
}

const maxFleetSystems = 20

func addFleet(s *mcp.Server, t *tools) {
	in := inputSchema[fleetIn]("fleet_status")
	array(in.Properties["systems"], 0, maxFleetSystems)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "fleet_status",
		Description: fleetDescription,
		InputSchema: in,
		Annotations: readOnly,
	}, instrument(t, "fleet_status", t.fleet))
}

func (t *tools) fleet(ctx context.Context, req *mcp.CallToolRequest, in fleetIn) (*mcp.CallToolResult, report.FleetResult, error) {
	now := t.d.Now()
	systems, ask, err := t.systems(ctx, req)
	if ask != nil || err != nil {
		return ask, report.FleetResult{}, err
	}
	picked, ask, err := resolveAll(req, systems, in.Systems)
	if ask != nil || err != nil {
		return ask, report.FleetResult{}, err
	}

	input := report.FleetInput{
		Now: now, HubVersion: t.d.HubVersion, TokenExpiresAt: t.d.TokenExpiresAt, Systems: picked,
		RecentContainers: map[string]report.Read[[]hub.ContainerStats]{}, LastSeen: map[string]report.Read[time.Time]{},
	}
	var fe fetcher
	read(ctx, &fe, &input.Services, t.d.Hub.Services)
	read(ctx, &fe, &input.Containers, t.d.Hub.Containers)
	read(ctx, &fe, &input.Smart, t.d.Hub.SmartDevices)
	read(ctx, &fe, &input.Pools, t.d.Hub.ZfsPools)
	read(ctx, &fe, &input.Monitors, t.d.Hub.Monitors)
	for _, s := range picked {
		if s.Status != hub.StatusUp {
			t.readLastSeen(ctx, &fe, &input, s)
			continue
		}
		readKey(ctx, &fe, input.RecentContainers, s.ID, func(ctx context.Context) ([]hub.ContainerStats, error) {
			return t.d.Hub.ContainerStats(ctx, s.ID, hub.Stat10m, now.Add(-time.Hour))
		})
	}
	fe.wait()
	return nil, report.Fleet(input, report.FleetOptions{ProblemsOnly: in.ProblemsOnly}), nil
}
