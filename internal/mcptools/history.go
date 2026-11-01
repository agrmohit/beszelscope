package mcptools

import (
	"context"
	"slices"

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
	in := inputSchema[historyIn]("metric_history")
	array(in.Properties["systems"], 1, report.MaxHistorySystems)
	array(in.Properties["metrics"], 1, report.MaxMetrics)
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
	picked, ask, err := resolveAll(req, systems, in.Systems)
	if ask != nil || err != nil {
		return ask, report.HistoryResult{}, err
	}

	input := report.HistoryInput{Now: now, TokenExpiresAt: t.d.TokenExpiresAt, Window: w, Systems: make([]report.SystemHistory, len(picked))}
	// One read per system and source, whatever the number of metrics. A
	// failed read empties only the series that need it, with a note. Each
	// read writes its own field, so they share no lock.
	since := now.Add(-w.Span)
	var fe fetcher
	if slices.Contains(sources, report.SourceMonitors) {
		read(ctx, &fe, &input.Monitors, t.d.Hub.Monitors)
	}
	for i, s := range picked {
		sh := &input.Systems[i]
		sh.System = s
		for _, src := range sources {
			switch src {
			case report.SourceStats:
				read(ctx, &fe, &sh.Stats, func(ctx context.Context) ([]hub.SystemStats, error) {
					return t.d.Hub.SystemStats(ctx, s.ID, w.Type, since)
				})
			case report.SourceContainers:
				read(ctx, &fe, &sh.Containers, func(ctx context.Context) ([]hub.ContainerStats, error) {
					return t.d.Hub.ContainerStats(ctx, s.ID, w.Type, since)
				})
			case report.SourceMonitors:
				read(ctx, &fe, &sh.MonitorStats, func(ctx context.Context) ([]hub.MonitorStats, error) {
					return t.d.Hub.MonitorStats(ctx, s.ID, w.Type, since)
				})
			}
		}
	}
	fe.wait()

	res, err := report.History(input, report.HistoryOptions{Metrics: metrics, Points: in.Points})
	if err != nil {
		return nil, report.HistoryResult{}, err
	}
	return nil, res, nil
}
