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
	in, err := jsonschema.For[fleetIn](nil)
	if err != nil {
		panic(fmt.Sprintf("fleet_status input schema: %v", err)) // fixed type; fails only on a code bug
	}
	in.Properties["systems"].Types, in.Properties["systems"].Type = nil, "array" // not ["null","array"]
	in.Properties["systems"].MaxItems = new(maxFleetSystems)
	in.Properties["systems"].UniqueItems = true
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
	ids, ask, err := resolveAll(req, systems, in.Systems)
	if ask != nil || err != nil {
		return ask, report.FleetResult{}, err
	}

	input := report.FleetInput{
		Now: now, HubVersion: t.d.HubVersion, TokenExpiresAt: t.d.TokenExpiresAt, Systems: systems,
		LastSeen: map[string]time.Time{},
	}
	var fe fetcher
	const lacks = "related problems may be missing"
	fe.run("systemd services", lacks, func() (err error) { input.Services, err = t.d.Hub.Services(ctx); return err })
	fe.run("containers", lacks, func() (err error) { input.Containers, err = t.d.Hub.Containers(ctx); return err })
	fe.run("SMART devices", lacks, func() (err error) { input.Smart, err = t.d.Hub.SmartDevices(ctx); return err })
	fe.run("storage pools", lacks, func() (err error) { input.Pools, err = t.d.Hub.ZfsPools(ctx); return err })
	fe.run("network monitors", lacks, func() (err error) { input.Monitors, err = t.d.Hub.Monitors(ctx); return err })
	for _, s := range systems {
		if len(ids) > 0 && !slices.Contains(ids, s.ID) {
			continue
		}
		if s.Status != hub.StatusUp {
			t.lastSeen(ctx, &fe, s, &input)
			continue
		}
		fe.run("container history of "+s.Name, lacks, func() error {
			cs, err := t.d.Hub.ContainerStats(ctx, s.ID, hub.Stat10m, now.Add(-time.Hour))
			fe.locked(func() { input.RecentContainers = append(input.RecentContainers, cs...) })
			return err
		})
	}
	warnings := fe.wait()

	res, err := report.Fleet(input, report.FleetOptions{Systems: ids, ProblemsOnly: in.ProblemsOnly})
	if err != nil {
		return nil, report.FleetResult{}, err
	}
	res.Warnings = append(res.Warnings, warnings...)
	return nil, res, nil
}

// lastSeen starts the read of when s last sent data. Beszel's updated time
// moves on record rewrites, so it is only the fallback.
func (t *tools) lastSeen(ctx context.Context, fe *fetcher, s hub.System, in *report.FleetInput) {
	fe.run("last data of "+s.Name, "its last-seen time is when its status last changed", func() error {
		seen, err := t.d.Hub.LastStatsAt(ctx, s.ID)
		if err == nil {
			fe.locked(func() { in.LastSeen[s.ID] = seen })
		}
		return err
	})
}

// resolveAll turns names into system IDs. For ambiguous names it returns
// a request asking the user, when the client can ask and has not answered.
func resolveAll(req *mcp.CallToolRequest, systems []hub.System, refs []string) ([]string, *mcp.CallToolResult, error) {
	ids := make([]string, 0, len(refs))
	questions := mcp.InputRequestMap{}
	for _, ref := range refs {
		s, err := report.Resolve(systems, ref)
		amb, ambiguous := errors.AsType[*report.AmbiguousError](err)
		switch {
		case err == nil:
			ids = append(ids, s.ID)
		case !ambiguous:
			return nil, nil, err
		default:
			id, answered := pickedID(req, amb)
			switch {
			case id != "":
				ids = append(ids, id)
			case !answered && canElicit(req, "form"):
				questions[pickID(ref)] = pickRequest(amb)
			default:
				return nil, nil, err // declined, or the client cannot ask
			}
		}
	}
	if len(questions) > 0 {
		return nil, &mcp.CallToolResult{InputRequests: questions}, nil
	}
	return ids, nil, nil
}
