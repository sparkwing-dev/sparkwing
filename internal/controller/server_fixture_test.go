package controller

import (
	"testing"

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

func engineServerFixture(t *testing.T, cfg deployment.Config, configure func(*Server)) (*engineFixture, *store.Tenant) {
	t.Helper()
	f := &serverFixture{}
	e, tenant := engineRuntimeStore(t, f.option(cfg, configure))
	e.Server = f.Server.WithEngineController(e.EngineController)
	return e, tenant
}
