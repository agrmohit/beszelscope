package mcptools

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agrmohit/beszelscope/internal/hub"
	"github.com/agrmohit/beszelscope/internal/report"
)

// Sent to the model as the tool description; rewording changes behaviour.
const historyDescription = "Trends for 1-5 servers monitored by the Beszel hub over the last 1h, 12h, 24h, 7d or 30d. " +
	"For each server and metric: the average, the lowest and highest bucket, the latest value, " +
	"and coverage (the share of the window with data; gaps mean the server or agent was down). " +
	"peak is a true per-minute maximum (cpu, mem, net, disk_io, monitor latency); " +
	"max_bucket_avg is the highest bucket average, so a short spike can be averaged away; use a shorter window to see it. " +
	"net and disk_io come as two series per direction, container:<name> as CPU and memory. " +
	"A metric target that does not exist returns an error listing the valid ones. " +
	"points adds a series to plot."

type historyIn struct {
	Systems []string `json:"systems" jsonschema:"Servers by name or ID, 1-5. Names are case-insensitive and a unique prefix works."`
	Metrics []string `json:"metrics" jsonschema:"1-6 metrics as kind or kind:target: cpu, cpu_iowait, mem, swap, disk[:mount], disk_io, net[:interface], load, temp[:sensor] (hottest sensor when no target), gpu[:id] (busiest GPU when no target), container:<name>, monitor:<target>."`
	Window  string   `json:"window,omitempty" jsonschema:"1h, 12h, 24h, 7d or 30d; 24h when omitted."`
	Points  int      `json:"points,omitempty" jsonschema:"Points per series to plot, as [age_s, value]; none when omitted."`
}

func addHistory(s *mcp.Server, t *tools) {
	in, err := jsonschema.For[historyIn](nil)
	if err != nil {
		panic(fmt.Sprintf("metric_history input schema: %v", err)) // fixed type; fails only on a code bug
	}
	for name, n := range map[string]int{"systems": report.MaxHistorySystems, "metrics": report.MaxMetrics} {
		p := in.Properties[name]
		p.Types, p.Type = nil, "array" // not ["null","array"]
		p.MinItems, p.MaxItems, p.UniqueItems = new(1), new(n), true
	}
	window := in.Properties["window"]
	for _, w := range report.Windows {
		window.Enum = append(window.Enum, w.Name)
	}
	points := in.Properties["points"]
	points.Minimum, points.Maximum = new(0.0), new(float64(report.MaxPoints))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "metric_history",
		Description: historyDescription,
		InputSchema: in,
		Annotations: readOnly,
	}, instrument(t, "metric_history", t.history))
}

func (t *tools) history(ctx context.Context, req *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, report.HistoryResult, error) {
	now := t.d.Now()
	metrics, sources, err := report.ParseMetrics(in.Metrics)
	if err != nil {
		return nil, report.HistoryResult{}, err
	}
	w, err := report.FindWindow(in.Window)
	if err != nil {
		return nil, report.HistoryResult{}, err
	}
	systems, ask, err := t.systems(ctx, req)
	if ask != nil || err != nil {
		return ask, report.HistoryResult{}, err
	}
	ids, ask, err := resolveAll(req, systems, in.Systems)
	if ask != nil || err != nil {
		return ask, report.HistoryResult{}, err
	}

	input := report.HistoryInput{
		Now: now, TokenExpiresAt: t.d.TokenExpiresAt, Window: w,
		Stats: map[string][]hub.SystemStats{}, Containers: map[string][]hub.ContainerStats{}, MonitorStats: map[string][]hub.MonitorStats{},
		StatsErrs: map[string]error{}, ContainerErrs: map[string]error{}, MonitorStatsErrs: map[string]error{},
	}
	for _, id := range ids {
		if !slices.ContainsFunc(input.Systems, func(s hub.System) bool { return s.ID == id }) {
			input.Systems = append(input.Systems, systems[slices.IndexFunc(systems, func(s hub.System) bool { return s.ID == id })])
		}
	}
	// One read per system and source, whatever the number of metrics. A
	// failed read empties only the series that need it, with a note.
	since := now.Add(-w.Span)
	var fe fetcher
	if slices.Contains(sources, report.SourceMonitors) {
		fe.run("network monitors", "", func() (err error) {
			input.Monitors, input.MonitorsErr = t.d.Hub.Monitors(ctx)
			return nil
		})
	}
	for _, s := range input.Systems {
		for _, src := range sources {
			fe.run("history", "", func() error {
				switch src {
				case report.SourceStats:
					rows, err := t.d.Hub.SystemStats(ctx, s.ID, w.Type, since)
					fe.locked(func() { input.Stats[s.ID], input.StatsErrs[s.ID] = rows, err })
				case report.SourceContainers:
					rows, err := t.d.Hub.ContainerStats(ctx, s.ID, w.Type, since)
					fe.locked(func() { input.Containers[s.ID], input.ContainerErrs[s.ID] = rows, err })
				case report.SourceMonitors:
					rows, err := t.d.Hub.MonitorStats(ctx, s.ID, w.Type, since)
					fe.locked(func() { input.MonitorStats[s.ID], input.MonitorStatsErrs[s.ID] = rows, err })
				}
				return nil
			})
		}
	}
	fe.wait() // every read records its own error

	res, err := report.History(input, report.HistoryOptions{Metrics: metrics, Points: in.Points})
	if err != nil {
		return nil, report.HistoryResult{}, err
	}
	return nil, res, nil
}
