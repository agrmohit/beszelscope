package mcptools

import (
	"crypto/rand"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

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

func pickLabels(amb *report.AmbiguousError) (labels []any, byLabel map[string]string) {
	byLabel = map[string]string{}
	for _, s := range amb.Matches {
		label := s.Name + " (" + s.ID + ")"
		byLabel[label] = s.ID
		labels = append(labels, label)
	}
	return labels, byLabel
}

// pickRequest asks which system an ambiguous name meant.
func pickRequest(amb *report.AmbiguousError) *mcp.ElicitParams {
	labels, _ := pickLabels(amb)
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

// pickedID returns the system the user chose for amb, if they answered.
func pickedID(req *mcp.CallToolRequest, amb *report.AmbiguousError) (id string, answered bool) {
	r, ok := answer(req, pickID(amb.Ref))
	if !ok {
		return "", false
	}
	if r.Action != "accept" {
		return "", true
	}
	label, _ := r.Content["system"].(string)
	_, byLabel := pickLabels(amb)
	return byLabel[label], true
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
