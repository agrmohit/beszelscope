package mcptools

import (
	"crypto/rand"
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agrmohit/beszelscope/internal/hub"
	"github.com/agrmohit/beszelscope/internal/report"
)

// Questions use the multi-round-trip pattern: a handler returns input
// requests, the client asks the user, then retries the call with the
// answers in InputResponses. The SDK translates this for older clients.

const renewID = "renew_token"

// canElicit reports whether the client accepts elicitation in mode ("form"
// or "url"). An empty elicitation capability means form, per the spec.
func canElicit(req *mcp.CallToolRequest, mode string) bool {
	caps := req.ClientCapabilities()
	if caps == nil || caps.Elicitation == nil {
		return false
	}
	e := caps.Elicitation
	if mode == "url" {
		return e.URL != nil
	}
	return e.Form != nil || e.URL == nil
}

func answer(req *mcp.CallToolRequest, id string) (*mcp.ElicitResult, bool) {
	r, ok := req.Params.InputResponses[id].(*mcp.ElicitResult)
	return r, ok
}

func pickID(ref string) string { return "pick_system:" + ref }

// pickRequest asks which system an ambiguous name meant.
func pickRequest(amb *report.AmbiguousError) *mcp.ElicitParams {
	labels := make([]any, len(amb.Matches))
	for i, s := range amb.Matches {
		labels[i] = report.Label(s)
	}
	return &mcp.ElicitParams{
		Mode:    "form",
		Message: fmt.Sprintf("%q matches several systems. Which one did you mean?", amb.Ref),
		RequestedSchema: &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{"system": {Type: "string", Title: "System", Enum: labels}},
			Required:   []string{"system"},
		},
	}
}

// chosen returns the system the user chose for amb, if they answered; nil
// when they declined or named none of the matches.
func chosen(req *mcp.CallToolRequest, amb *report.AmbiguousError) (s *hub.System, answered bool) {
	r, ok := answer(req, pickID(amb.Ref))
	if !ok {
		return nil, false
	}
	if r.Action != "accept" {
		return nil, true
	}
	label, _ := r.Content["system"].(string)
	if i := slices.IndexFunc(amb.Matches, func(s hub.System) bool { return report.Label(s) == label }); i >= 0 {
		return &amb.Matches[i], true
	}
	return nil, true
}

// renewalRequest offers, once per process, to open the hub's user admin so
// a human can mint a new token. The token itself never passes through MCP:
// the spec forbids collecting secrets by elicitation.
func (t *tools) renewalRequest(req *mcp.CallToolRequest) *mcp.CallToolResult {
	if _, answered := answer(req, renewID); answered || !canElicit(req, "url") || !t.renewAsked.CompareAndSwap(false, true) {
		return nil
	}
	page := t.d.HubURL.JoinPath("_/").String() + "#/collections?collection=users"
	return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{renewID: &mcp.ElicitParams{
		Mode: "url",
		Message: "The Beszel hub rejected beszelscope's token. Open the hub's user admin to mint a new token " +
			"for the beszelscope user, then run `beszelscope token set`.",
		URL:           page,
		ElicitationID: rand.Text(),
	}}}
}
