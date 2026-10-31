package mcptools

import (
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
)

// fetcher runs hub reads in parallel and collects their failures as
// warnings, so one failed read leaves a partial result instead of none.
type fetcher struct {
	mu       sync.Mutex
	wg       sync.WaitGroup
	warnings []string
	crashed  *goroutinePanic
}

// run starts f. If f fails, the warning names what was read and what the
// result therefore lacks.
func (fe *fetcher) run(what, lacks string, f func() error) {
	fe.wg.Go(func() {
		// instrument recovers only the handler goroutine, so a panic here
		// is carried back and re-raised there.
		defer func() {
			if r := recover(); r != nil {
				fe.mu.Lock()
				fe.crashed = &goroutinePanic{value: r, stack: debug.Stack()}
				fe.mu.Unlock()
			}
		}()
		if err := f(); err != nil {
			fe.mu.Lock()
			fe.warnings = append(fe.warnings, fmt.Sprintf("could not read %s, so %s: %v", what, lacks, err))
			fe.mu.Unlock()
		}
	})
}

// locked runs f while holding the fetcher's lock, for appends from reads.
func (fe *fetcher) locked(f func()) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	f()
}

// wait blocks until every read is done and returns the warnings, sorted.
// It re-raises a panic from any read.
func (fe *fetcher) wait() []string {
	fe.wg.Wait()
	if fe.crashed != nil {
		panic(fe.crashed)
	}
	slices.Sort(fe.warnings)
	return fe.warnings
}
