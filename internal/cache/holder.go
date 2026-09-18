// Package cache holds the current graph snapshot and persists pool state for
// warm starts.
//
// The two things here serve different purposes and must not be confused. The
// Holder is on the request path and is built for lock-free reads. The Store is
// not on the request path at all: it exists so a restarting instance can serve
// traffic before its first RPC refresh completes, and so sibling instances can
// share the cost of indexing.
package cache

import (
	"sync/atomic"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/graph"
)

// neverSet is the age reported before any snapshot has been published. It is
// large enough that any staleness check treats the service as not ready.
const neverSet = 100 * 365 * 24 * time.Hour

// Holder publishes the current snapshot to readers.
//
// Reads go through a single atomic pointer load. There is no mutex, so a slow
// or numerous set of readers cannot delay a refresh, and a refresh cannot
// stall requests. Because a Snapshot is immutable once built, a reader that
// loads the pointer can keep using that graph for the rest of its request even
// if a newer one is published a microsecond later. That is the property that
// makes a consistent quote possible without holding a lock across the search.
type Holder struct {
	current atomic.Pointer[graph.Snapshot]
}

// NewHolder returns an empty holder.
func NewHolder() *Holder { return &Holder{} }

// Get returns the current snapshot, or nil if none has been published.
func (h *Holder) Get() *graph.Snapshot { return h.current.Load() }

// Set publishes a new snapshot.
//
// A nil snapshot is ignored rather than published. A refresh that failed should
// leave the previous graph serving: slightly stale quotes are far better than
// no quotes, and the failure is reported through the indexer's error and the
// snapshot age instead.
func (h *Holder) Set(s *graph.Snapshot) {
	if s == nil {
		return
	}
	h.current.Store(s)
}

// IsEmpty reports whether any snapshot has been published yet.
func (h *Holder) IsEmpty() bool { return h.current.Load() == nil }

// Age returns how long ago the current snapshot was built, or an effectively
// infinite duration if there is none.
func (h *Holder) Age() time.Duration {
	s := h.current.Load()
	if s == nil {
		return neverSet
	}
	return s.Age()
}
