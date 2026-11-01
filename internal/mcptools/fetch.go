package mcptools

import (
	"context"
	"runtime/debug"
	"sync"

	"github.com/agrmohit/beszelscope/internal/report"
)

// fetcher runs hub reads in parallel. Each read stores its own result, so
// one failed read leaves a partial result instead of none.
type fetcher struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	crashed *goroutinePanic
}

func (fe *fetcher) run(f func()) {
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
		f()
	})
}

// wait blocks until every read is done. It re-raises a panic from any read.
func (fe *fetcher) wait() {
	fe.wg.Wait()
	if fe.crashed != nil {
		panic(fe.crashed)
	}
}

// read starts f and stores its result in dst.
func read[T any](ctx context.Context, fe *fetcher, dst *report.Read[T], f func(context.Context) (T, error)) {
	fe.run(func() {
		v, err := f(ctx)
		*dst = report.Read[T]{Val: v, Err: err}
	})
}

// readKey starts f and stores its result in m under key. Reads share m, so
// the store takes the fetcher's lock.
func readKey[T any](ctx context.Context, fe *fetcher, m map[string]report.Read[T], key string, f func(context.Context) (T, error)) {
	fe.run(func() {
		v, err := f(ctx)
		fe.mu.Lock()
		defer fe.mu.Unlock()
		m[key] = report.Read[T]{Val: v, Err: err}
	})
}
