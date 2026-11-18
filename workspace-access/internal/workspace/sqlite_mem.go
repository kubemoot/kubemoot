package workspace

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// maxConcurrentQueries bounds how many SQLite queries run at once, so the memory
	// they can add together has a ceiling.
	maxConcurrentQueries = 2
	// memoryPoll is how often the memory watch reads the process size.
	memoryPoll = 20 * time.Millisecond
)

var (
	errMemory = errors.New("the query used more memory than allowed")
	// slots holds one token per query that may run.
	slots = make(chan struct{}, maxConcurrentQueries)
)

// acquireSlot waits for a free query slot or for ctx to end, and returns the release.
func acquireSlot(ctx context.Context) (func(), error) {
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// watchMemory ends the returned context when the process has grown by more than
// limit bytes since the call. This SQLite build keeps its data outside the Go heap and
// does not enforce a heap limit, so the size of the process is the one measure of what
// a query holds. Where the size cannot be read (no /proc), nothing is watched and the
// other limits apply. Call the stop function when the query ends.
func watchMemory(ctx context.Context, limit int64) (context.Context, func()) {
	base, ok := residentBytes()
	ctx, cancel := context.WithCancelCause(ctx)
	if !ok || limit <= 0 {
		return ctx, func() { cancel(nil) }
	}
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(memoryPoll)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if now, ok := residentBytes(); ok && now-base > limit {
					cancel(errMemory)
					return
				}
			}
		}
	}()
	return ctx, func() { close(done); cancel(nil) }
}

// residentBytes reads the resident size of this process from /proc.
func residentBytes() (int64, bool) {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}
