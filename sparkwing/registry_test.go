package sparkwing

import "testing"

func isolateRegistry(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	prior := registry
	registry = map[string]*registration{}
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registry = prior
		registryMu.Unlock()
	})
}

func TestRegistrationRejectsDuplicateNames(t *testing.T) {
	for name, register := range map[string]func(){
		"pipeline":   func() { Register("duplicate", func() Pipeline[NoInputs] { return replayFixture{} }) },
		"entrypoint": func() { RegisterEntrypoint("duplicate", func() Pipeline[NoInputs] { return replayFixture{} }) },
	} {
		t.Run(name, func(t *testing.T) {
			isolateRegistry(t)
			register()
			defer func() {
				if recover() == nil {
					t.Fatal("duplicate registration succeeded")
				}
			}()
			register()
		})
	}
}
