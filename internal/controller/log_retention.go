package controller

import (
	"maps"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// WithLogRetention keeps the log segments of the named teams for the
// given number of days instead of the built-in 90 (with credits) or 30
// (without). Each distinct number is a retention class under
// logs/r<days>/, which needs its own lifecycle rule in the bucket.
func (s *Server) WithLogRetention(days map[store.Team]int) *Server {
	s.logRetention = maps.Clone(days)
	return s
}
