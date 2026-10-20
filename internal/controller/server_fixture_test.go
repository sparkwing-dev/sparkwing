package controller

import (
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type serverFixture struct {
	*Server
	store  *store.Store
	engine *engineFixture
}

func (f *serverFixture) option(cfg deployment.Config, configure func(*Server)) store.Option {
	return store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		f.store = st
		f.Server = NewDeployment(st, cfg, nil)
		if configure != nil {
			configure(f.Server)
		}
		return f.StorePolicies(), nil
	})
}

func (s *Server) fixtureServer() *Server { return s }
