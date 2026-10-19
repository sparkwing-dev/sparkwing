package main

import (
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerhost"
)

func TestNoDeploymentComposesTheControllerHost(t *testing.T) {
	root := t.TempDir()
	st, err := openControllerStore(t.Context(), filepath.Join(root, "state.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := newControllerServer(st, root, "development", nil)
	if err != nil {
		t.Fatal(err)
	}
	host, err := controllerhost.New(t.Context(), srv, hostConfig(root))
	var live int
	if err == nil {
		err = st.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE subject_id='controller-host'`).Scan(&live)
	}
	if err != nil || host != nil || live != 0 {
		t.Fatalf("the controller host must stay off: %v, %d roots, %v", host, live, err)
	}
}
