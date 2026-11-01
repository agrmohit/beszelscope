//go:build !darwin

package main

import (
	"strings"
	"testing"
)

// Off macOS the token commands are not built: token is an unknown command
// and the help does not offer it.
func TestTokenNotBuilt(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "bare", args: []string{"token"}},
		{name: "set", args: []string{"token", "set"}},
		{name: "help", args: []string{"token", "--help"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := runCmd(t, tt.args, nil, "", nil)
			r.check(t, `unknown command "token"`, nil, nil)
		})
	}
	t.Run("help omits token", func(t *testing.T) {
		r := runCmd(t, []string{"--help"}, nil, "", nil)
		r.check(t, "", []string{"beszelscope check"}, nil)
		if strings.Contains(r.stdout, "token ...") {
			t.Fatalf("help offers token commands:\n%s", r.stdout)
		}
	})
}
