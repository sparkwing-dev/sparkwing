package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func ExampleOpen() {
	dir, err := os.MkdirTemp("", "sparkwing-store-")
	if err != nil {
		fmt.Println("directory:", err)
		return
	}
	defer os.RemoveAll(dir)
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	defer s.Close()
	tenant, err := s.ForTeam(context.Background(), store.DefaultTeam)
	if err != nil {
		fmt.Println("team:", err)
		return
	}
	fmt.Println("team:", tenant.Team())
	// Output: team: default
}
