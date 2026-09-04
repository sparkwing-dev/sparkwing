package controller_test

import (
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func ExampleNew() {
	dir, _ := os.MkdirTemp("", "sparkwing-controller-")
	defer os.RemoveAll(dir)
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		fmt.Println("store:", err)
		return
	}
	defer st.Close()

	srv := controller.New(st, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	fmt.Println("controller routes mounted")
	// Output: controller routes mounted
}

type passthroughCipher struct{}

func (passthroughCipher) Seal(plain string) (string, error) {
	return "raw:" + base64.StdEncoding.EncodeToString([]byte(plain)), nil
}

func (passthroughCipher) Open(envelope string) (string, error) {
	const prefix = "raw:"
	if len(envelope) < len(prefix) || envelope[:len(prefix)] != prefix {
		return "", fmt.Errorf("not a passthrough envelope")
	}
	b, err := base64.StdEncoding.DecodeString(envelope[len(prefix):])
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func ExampleServer_WithSecretsCipher() {
	dir, _ := os.MkdirTemp("", "sparkwing-cipher-")
	defer os.RemoveAll(dir)
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		fmt.Println("store:", err)
		return
	}
	defer st.Close()

	srv := controller.New(st, nil).WithSecretsCipher(passthroughCipher{})
	_ = srv
	fmt.Println("server has cipher wired")
	// Output: server has cipher wired
}
