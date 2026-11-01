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

// resolveAll returns the systems refs name, in request order and each once;
// no refs means all. For ambiguous names it returns a request asking the
// user, when the client can ask and has not answered.
func resolveAll(req *mcp.CallToolRequest, systems []hub.System, refs []string) ([]hub.System, *mcp.CallToolResult, error) {
	if len(refs) == 0 {
		return systems, nil, nil
	}
	var picked []hub.System
	add := func(s hub.System) {
		if !slices.ContainsFunc(picked, func(p hub.System) bool { return p.ID == s.ID }) {
			picked = append(picked, s)
		}
	}
	questions := mcp.InputRequestMap{}
	for _, ref := range refs {
		s, err := report.Resolve(systems, ref)
		amb, ambiguous := errors.AsType[*report.AmbiguousError](err)
		switch {
		case err == nil:
			add(s)
		case !ambiguous:
			return nil, nil, err
		default:
			s, answered := chosen(req, amb)
			switch {
			case s != nil:
				add(*s)
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
	return picked, nil, nil
}

// readLastSeen starts the read of when s last sent data. Beszel's updated
// time moves on record rewrites, so it is only the fallback.
func (t *tools) readLastSeen(ctx context.Context, fe *fetcher, in *report.FleetInput, s hub.System) {
	readKey(ctx, fe, in.LastSeen, s.ID, func(ctx context.Context) (time.Time, error) { return t.d.Hub.LastStatsAt(ctx, s.ID) })
}

// inputSchema is T's input schema. T is a fixed type, so an error is a code bug.
func inputSchema[T any](tool string) *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("%s input schema: %v", tool, err))
	}
	return s
}

// array makes p an array of minItems to maxItems unique items; 0 minItems
// sets no minimum.
func array(p *jsonschema.Schema, minItems, maxItems int) {
	p.Types, p.Type = nil, "array" // not ["null","array"]
	if minItems > 0 {
		p.MinItems = new(minItems)
	}
	p.MaxItems, p.UniqueItems = new(maxItems), true
}

// enum lists vs as schema values. Validation compares the decoded JSON, a string.
func enum[T ~string](vs []T) []any {
	out := make([]any, len(vs))
	for i, v := range vs {
		out[i] = string(v)
	}
	return out
}
