package logs

import "time"

// NodeAttemptPath exposes the attempt-scoped log path to the external tests.
var NodeAttemptPath = nodeAttemptPath

// SetDiskSpace replaces the free-space probe the write path consults.
func (s *Server) SetDiskSpace(fn func(path string) (free, total uint64, ok bool)) { s.diskSpace = fn }

// SetAuthCacheTTL sets how long a resolved caller is cached.
func (s *Server) SetAuthCacheTTL(d time.Duration) { s.authCacheTTL = d }

// AuthCached reports whether a credential's resolution is cached.
func (s *Server) AuthCached(credential string) bool {
	_, ok := s.authCache.Load(credential)
	return ok
}
