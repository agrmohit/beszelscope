package mcptools

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agrmohit/beszelscope/internal/hub"
	"github.com/agrmohit/beszelscope/internal/report"
)

// Sent to the model as the tool description; rewording changes behaviour.
const fleetDescription = "Health of every server monitored by the Beszel hub: one compact row per host with " +
	"status, CPU/memory/fullest-disk %, hottest temperature, load per thread, network bytes/s, uptime and data age. " +
	"Lists problems: down/paused/pending, stale data, failed systemd units, unhealthy or stopped containers, " +
	"SMART and storage-pool faults, monitor packet loss, expiring certificates and pending security updates. " +
	`Start here for "is anything wrong" or "how are my servers". Each problem names the section of ` +
	"system_detail that explains it. Values are from the latest 60 s poll, not history."

type fleetIn struct {
	Systems      []string `json:"systems,omitempty" jsonschema:"Names or IDs to include; omit for all systems."`
	ProblemsOnly bool     `json:"problems_only,omitempty" jsonschema:"Return only systems with problems; counts still cover all."`
}

const maxFleetSystems = 20

func addFleet(s *mcp.Server, t *tools) {
	in, err := jsonschema.For[fleetIn](nil)
	if err != nil {
		panic(fmt.Sprintf("fleet_status input schema: %v", err)) // fixed type; fails only on a code bug
	}
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
	systems, err := t.d.Hub.Systems(ctx)
	if err != nil {
		if errors.Is(err, hub.ErrUnauthorized) {
			if ask := t.renewalRequest(req); ask != nil {
				return ask, report.FleetResult{}, nil
			}
		}
		return nil, report.FleetResult{}, hubError(err)
	}
	ids, ask, err := resolveAll(req, systems, in.Systems)
	if ask != nil || err != nil {
		return ask, report.FleetResult{}, err
	}

	input := report.FleetInput{
		Now: now, HubVersion: t.d.HubVersion, TokenExpiresAt: t.d.TokenExpiresAt, Systems: systems,
	}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		warnings []string
		crashed  *goroutinePanic
	)
	fetch := func(what string, f func() error) {
		wg.Go(func() {
			// instrument recovers only the handler goroutine, so a panic here
			// is carried back and re-raised there.
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					crashed = &goroutinePanic{value: r, stack: debug.Stack()}
					mu.Unlock()
				}
			}()
			if err := f(); err != nil {
				mu.Lock()
				warnings = append(warnings, fmt.Sprintf("could not read %s, so related problems may be missing: %v", what, err))
				mu.Unlock()
			}
		})
	}
	fetch("systemd services", func() (err error) { input.Services, err = t.d.Hub.Services(ctx); return err })
	fetch("containers", func() (err error) { input.Containers, err = t.d.Hub.Containers(ctx); return err })
	fetch("SMART devices", func() (err error) { input.Smart, err = t.d.Hub.SmartDevices(ctx); return err })
	fetch("storage pools", func() (err error) { input.Pools, err = t.d.Hub.ZfsPools(ctx); return err })
	fetch("network monitors", func() (err error) { input.Monitors, err = t.d.Hub.Monitors(ctx); return err })
	for _, s := range systems {
		if s.Status != "up" || (len(ids) > 0 && !slices.Contains(ids, s.ID)) {
			continue
		}
		fetch("container history of "+s.Name, func() error {
			cs, err := t.d.Hub.ContainerStats(ctx, s.ID, hub.Stat10m, now.Add(-time.Hour))
			mu.Lock()
			input.RecentContainers = append(input.RecentContainers, cs...)
			mu.Unlock()
			return err
		})
	}
	wg.Wait()
	if crashed != nil {
		panic(crashed)
	}

	res, err := report.Fleet(input, report.FleetOptions{Systems: ids, ProblemsOnly: in.ProblemsOnly})
	if err != nil {
		return nil, report.FleetResult{}, err
	}
	slices.Sort(warnings)
	res.Warnings = append(res.Warnings, warnings...)
	return nil, res, nil
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
