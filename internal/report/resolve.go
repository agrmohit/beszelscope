// Package report turns hub records into tool output. It is pure: callers
// pass the records and the current time in.
package report

import (
	"fmt"
	"slices"
	"strings"

	"github.com/agrmohit/beszelscope/internal/hub"
)

// Resolve finds the system ref names: an exact ID, else a case-insensitive
// exact name, else a unique case-insensitive name prefix.
func Resolve(systems []hub.System, ref string) (hub.System, error) {
	if ref == "" {
		return hub.System{}, fmt.Errorf("empty system name; known: %s", names(systems))
	}
	for _, s := range systems {
		if s.ID == ref {
			return s, nil
		}
	}
	matches := func(ok func(name string) bool) []hub.System {
		var out []hub.System
		for _, s := range systems {
			if ok(strings.ToLower(s.Name)) {
				out = append(out, s)
			}
		}
		return out
	}
	lower := strings.ToLower(ref)
	for _, found := range [][]hub.System{
		matches(func(n string) bool { return n == lower }),
		matches(func(n string) bool { return strings.HasPrefix(n, lower) }),
	} {
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		default:
			return hub.System{}, &AmbiguousError{Ref: ref, Matches: found}
		}
	}
	return hub.System{}, fmt.Errorf("no system %q; known: %s", ref, names(systems))
}

// AmbiguousError means a name matched more than one system.
type AmbiguousError struct {
	Ref     string
	Matches []hub.System
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%q matches several systems: %s; use one of these IDs", e.Ref, withIDs(e.Matches))
}

func names(systems []hub.System) string {
	n := make([]string, len(systems))
	for i, s := range systems {
		n[i] = s.Name
	}
	slices.Sort(n)
	return strings.Join(n, ", ")
}

func withIDs(systems []hub.System) string {
	n := make([]string, len(systems))
	for i, s := range systems {
		n[i] = s.Name + " (" + s.ID + ")"
	}
	slices.Sort(n)
	return strings.Join(n, ", ")
}
