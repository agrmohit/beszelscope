package mcptools

import (
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// TestResolveAll covers name selection without ambiguity; asking the user
// is TestAmbiguousNameElicitation.
func TestResolveAll(t *testing.T) {
	systems := []hub.System{
		{ID: "aaaaaaaaaaaaaa1", Name: "web", Status: hub.StatusUp},
		{ID: "aaaaaaaaaaaaaa2", Name: "db", Status: "down"},
		{ID: "aaaaaaaaaaaaaa3", Name: "old", Status: "paused"},
	}
	tests := []struct {
		name    string
		refs    []string
		want    []string // system names, in order
		wantErr string
	}{
		{name: "no refs means all", want: []string{"web", "db", "old"}},
		{name: "empty refs means all", refs: []string{}, want: []string{"web", "db", "old"}},
		{name: "named systems ignore case", refs: []string{"WEB", "db"}, want: []string{"web", "db"}},
		{name: "request order kept", refs: []string{"old", "web"}, want: []string{"old", "web"}},
		{name: "name and ID of one system counted once", refs: []string{"web", "aaaaaaaaaaaaaa1"}, want: []string{"web"}},
		{name: "same name twice counted once", refs: []string{"db", "db"}, want: []string{"db"}},
		{name: "unknown name", refs: []string{"mail"}, wantErr: `"mail"`},
		{name: "unknown name after a known one", refs: []string{"web", "mail"}, wantErr: `"mail"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{}}
			picked, ask, err := resolveAll(req, systems, tt.refs)
			if ask != nil {
				t.Fatalf("unexpected question %+v", ask)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %s", err, tt.wantErr)
				}
				return
			}
			var got []string
			for _, s := range picked {
				got = append(got, s.Name)
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q (err %v), want %q", got, err, tt.want)
			}
		})
	}
}
