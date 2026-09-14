package web

import (
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ViewerIdentityPrefix labels a logs read the dashboard made for one
// browser session. The logs service budgets concurrent reads and streams
// per identity, and a dashboard serves every tab through one token, so
// without a per-viewer name one viewer's tabs spend the cap for everyone
// the deployment serves.
const ViewerIdentityPrefix = "dashboard:"

// TabHeaderName is the header a dashboard tab sets to be counted apart
// from the session's other tabs. Its value is an opaque id the browser
// picks; see [ValidTabID] for what one may look like.
const TabHeaderName = "X-Sparkwing-Tab"

// MaxTabIDLen bounds the tab id a browser may send.
const MaxTabIDLen = 32

// MaxTabsPerSession is how many tabs of one session are counted apart.
// Past it the least recently seen tab's slot is reused, so a viewer that
// rotates tab ids cycles a fixed set of names rather than minting one
// per request; the logs meter tracks a bounded number of principals and
// a viewer who filled it would fold every other principal into one
// shared overflow budget.
const MaxTabsPerSession = 16

// safety: bounds the sessions this tracker remembers, for the same
// reason MaxTabsPerSession bounds tabs.
const maxTrackedSessions = 4096

// ValidTabID reports whether a browser-supplied tab id is one this
// dashboard will label a read with: at most [MaxTabIDLen] characters of
// letters, digits, dot, underscore or dash. The character set keeps a
// CR or LF out of an outgoing header, and the length keeps one viewer
// from spending the logs meter's memory.
func ValidTabID(tab string) bool {
	if tab == "" || len(tab) > MaxTabIDLen {
		return false
	}
	for _, c := range tab {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// safety: assigning each tab a slot number bounds the identities one
// session can produce, however many tab ids its browser invents.
type viewerTabs struct {
	mu       sync.Mutex
	sessions map[string]*sessionTabs
	// safety: a counter rather than a clock, because the order two tabs
	// were last seen in is all the eviction needs and a monotonic counter
	// cannot go backwards the way a wall clock can.
	tick uint64
}

type sessionTabs struct {
	slots []tabSlot
	seen  uint64
}

type tabSlot struct {
	tab  string
	seen uint64
}

func newViewerTabs() *viewerTabs {
	return &viewerTabs{sessions: make(map[string]*sessionTabs)}
}

// safety: a full session reuses the least recently seen tab's slot, so
// the answer is bounded rather than growing with the ids a browser sends.
func (v *viewerTabs) slot(session, tab string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.tick++

	s := v.sessions[session]
	if s == nil {
		v.evictSessionLocked()
		s = &sessionTabs{}
		v.sessions[session] = s
	}
	s.seen = v.tick

	for i := range s.slots {
		if s.slots[i].tab == tab {
			s.slots[i].seen = v.tick
			return i
		}
	}
	if len(s.slots) < MaxTabsPerSession {
		s.slots = append(s.slots, tabSlot{tab: tab, seen: v.tick})
		return len(s.slots) - 1
	}
	oldest := 0
	for i := range s.slots {
		if s.slots[i].seen < s.slots[oldest].seen {
			oldest = i
		}
	}
	s.slots[oldest] = tabSlot{tab: tab, seen: v.tick}
	return oldest
}

func (v *viewerTabs) evictSessionLocked() {
	if len(v.sessions) < maxTrackedSessions {
		return
	}
	oldest, oldestSeen := "", uint64(0)
	for name, s := range v.sessions {
		if oldest == "" || s.seen < oldestSeen {
			oldest, oldestSeen = name, s.seen
		}
	}
	delete(v.sessions, oldest)
}

// safety: the logs service trusts this header for counting alone, never
// for authorization, so a session name is safe on it. Any inbound value
// is dropped first, on every path, so a client cannot name itself and
// spend another viewer's budget or reach a controller route with one.
func withViewerIdentity(tabs *viewerTabs, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// safety: the raw value is judged, not a trimmed copy, because the
		// reverse proxy forwards this header as it arrived and a value of
		// whitespace alone is one Go refuses to put on the wire.
		tab := r.Header.Get(TabHeaderName)
		if tab != "" && !ValidTabID(tab) {
			writeErr(w, http.StatusBadRequest, errors.New(TabHeaderName+
				" must be at most "+strconv.Itoa(MaxTabIDLen)+
				" characters of letters, digits, dot, underscore or dash"))
			return
		}
		ctx := r.Context()
		r = r.Clone(ctx)
		r.Header.Del(store.RunnerIdentityHeader)
		if id := viewerIdentity(tabs, r, tab); id != "" {
			r = r.WithContext(logs.WithReaderIdentity(ctx, id))
		}
		next.ServeHTTP(w, r)
	})
}

// safety: only the logs routes are metered per identity, so only they
// carry the header; a controller route that gained a per-runner budget
// must never see the dashboard's own name on a viewer's request.
func withLogsIdentityHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := logs.ReaderIdentityFromContext(r.Context()); id != "" {
			r = r.Clone(r.Context())
			r.Header.Set(store.RunnerIdentityHeader, id)
		}
		next.ServeHTTP(w, r)
	})
}

// safety: a browser that names its tab gets its own slot; one that does
// not shares its session's, which is still per viewer rather than per
// deployment.
func viewerIdentity(tabs *viewerTabs, r *http.Request, tab string) string {
	p, ok := WebPrincipalFromContext(r.Context())
	if !ok || p == nil || p.Name == "" {
		return ""
	}
	id := ViewerIdentityPrefix + p.Name
	if tab != "" && tabs != nil {
		id += "/" + strconv.Itoa(tabs.slot(p.Name, tab))
	}
	return id
}
