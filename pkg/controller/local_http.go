package controller

import (
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// HTTP supplies shared policy handlers for the elected local runtime.
func (s *Server) HTTP() wingd.LocalHTTP {
	s.localHTTPOnce.Do(func() {
		s.WithPeerIdentity(func(r *http.Request) capability.Subject {
			peer, ok := wingd.LocalPeer(r.Context())
			if !ok {
				return capability.Subject{}
			}
			switch peer.GOOS {
			case "linux", "darwin":
				if peer.UID != nil {
					return capability.Subject{Kind: capability.SubjectLocal, ID: fmt.Sprintf("local:%d", *peer.UID)}
				}
			case "windows":
				if peer.SID != "" {
					return capability.Subject{Kind: capability.SubjectLocal, ID: "local:" + peer.SID}
				}
			}
			return capability.Subject{}
		})
		router := s.routers()
		router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			policy := s.routeProbe()
			writeRouteMiss(w, allowedRouteMethods(policy, r, routeProbeMethods))
		})
		api := s.observedHandler(router)
		dashboard := api
		if s.dashboard != nil {
			dashboard = s.browserHandler(api)
		}
		s.localHTTP = wingd.LocalHTTP{API: api, Dashboard: dashboard}
	})
	return s.localHTTP
}
