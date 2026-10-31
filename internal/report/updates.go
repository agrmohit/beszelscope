package report

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// UpdatesSection is Beszel's pending package updates for one system. The
// list is a live answer from the agent, which serves its last hourly check.
type UpdatesSection struct {
	Unavailable string       `json:"unavailable,omitempty"` // why the package list is missing
	Total       *int         `json:"total,omitempty"`
	Security    *int         `json:"security,omitempty"` // absent when the package manager does not flag security updates
	Manager     string       `json:"manager,omitempty"`
	CheckedAgoS *int64       `json:"checked_ago_s,omitempty"`
	Packages    []PackageRow `json:"packages,omitempty"` // security updates first, then by name
	Absent      []string     `json:"absent,omitempty"`
}

// PackageRow is one pending update.
type PackageRow struct {
	Name      string `json:"name"`
	Current   string `json:"current,omitempty"` // empty when the manager does not say
	Available string `json:"available"`
	Security  bool   `json:"security,omitempty"`
}

// notChecked explains an agent that sends no package data at all. Naming
// only the cause that applies matters: given both, models picked the wrong one.
func notChecked(d *hub.SystemDetails) string {
	switch {
	case d == nil:
		return "updates: this agent does not check packages; Beszel checks them only on agents running natively on Linux, not in a container"
	case d.OS != 0:
		return "updates: Beszel checks packages only on Linux; this host runs " + osName(*d)
	default:
		return "updates: this Linux agent does not check packages; it runs in a container, has checks turned off, or found no supported package manager"
	}
}

func updates(in DetailInput) *UpdatesSection {
	if u := unavailable(in.System, in.Now); u != "" {
		return &UpdatesSection{Unavailable: u}
	}
	if in.PackagesErr != nil {
		// The counts on the systems row come from the same agent check.
		out := &UpdatesSection{Unavailable: "could not ask the agent for the package list: " + in.PackagesErr.Error()}
		if pu := in.System.Info.PackageUpdates; len(pu) > 0 {
			out.Total = new(int(pu[0]))
			if len(pu) > 1 {
				out.Security = new(int(pu[1]))
			}
		}
		return out
	}
	p := in.Packages
	switch {
	case p == nil || p.Manager == "" && p.CheckedAt == 0:
		return &UpdatesSection{Absent: []string{notChecked(in.Details)}}
	case p.CheckedAt == 0:
		return &UpdatesSection{Manager: p.Manager, Absent: []string{"updates: the agent's first package check has not finished"}}
	}
	out := &UpdatesSection{
		Total: new(len(p.Packages)), Manager: p.Manager,
		CheckedAgoS: new(ageS(in.Now, time.Unix(p.CheckedAt, 0))),
	}
	security := 0
	for _, pkg := range p.Packages {
		out.Packages = append(out.Packages, PackageRow{Name: pkg.Name, Current: pkg.Current, Available: pkg.Available, Security: pkg.Security})
		if pkg.Security {
			security++
		}
	}
	if p.SecurityKnown {
		out.Security = &security
	}
	slices.SortStableFunc(out.Packages, func(a, b PackageRow) int {
		if a.Security != b.Security {
			if a.Security {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}
