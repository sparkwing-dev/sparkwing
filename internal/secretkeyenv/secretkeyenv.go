// Package secretkeyenv keeps the local secrets key variables out of the
// environment a pipeline's steps inherit. A process that runs pipeline code
// calls [Hold] first; the variables then live in its memory, and only the
// children that need the key, the admission daemon and a node process, get
// them back through [Environ].
package secretkeyenv

import (
	"os"
	"strings"
	"sync"
)

const (
	keyName         = "SPARKWING_SECRETS_KEY"
	previousKeyName = "SPARKWING_SECRETS_PREVIOUS_KEY"
)

// Names are the variables that carry a local secrets key.
var Names = []string{keyName, previousKeyName}

var (
	mu   sync.Mutex
	held = map[string]string{}
)

// Hold moves every key variable set in the environment into this process's
// memory and unsets it, so a command the process starts without asking for
// the key inherits none.
func Hold() error {
	mu.Lock()
	defer mu.Unlock()
	// safety: each variable is read by its literal name so the docs check that
	// every variable the code reads is documented can see both.
	for _, v := range []struct{ name, value string }{
		{keyName, os.Getenv("SPARKWING_SECRETS_KEY")},
		{previousKeyName, os.Getenv("SPARKWING_SECRETS_PREVIOUS_KEY")},
	} {
		if v.value != "" {
			held[v.name] = v.value
		}
		if err := os.Unsetenv(v.name); err != nil {
			return err
		}
	}
	return nil
}

// Key returns SPARKWING_SECRETS_KEY from the environment, or the value
// [Hold] took.
func Key() string {
	if v := os.Getenv("SPARKWING_SECRETS_KEY"); v != "" {
		return v
	}
	return heldValue(keyName)
}

// PreviousKey returns SPARKWING_SECRETS_PREVIOUS_KEY from the environment,
// or the value [Hold] took.
func PreviousKey() string {
	if v := os.Getenv("SPARKWING_SECRETS_PREVIOUS_KEY"); v != "" {
		return v
	}
	return heldValue(previousKeyName)
}

func heldValue(name string) string {
	mu.Lock()
	defer mu.Unlock()
	return held[name]
}

// Forget drops what [Hold] took, for a process that has loaded its key and
// hands it to no child.
func Forget() {
	mu.Lock()
	defer mu.Unlock()
	held = map[string]string{}
}

// Environ returns env with the key variables [Hold] took set again, for a
// child that needs the key.
func Environ(env []string) []string {
	mu.Lock()
	defer mu.Unlock()
	if len(held) == 0 {
		return env
	}
	out := make([]string, 0, len(env)+len(held))
	for _, kv := range env {
		if !isKeyVar(kv) {
			out = append(out, kv)
		}
	}
	for _, name := range Names {
		if v, ok := held[name]; ok {
			out = append(out, name+"="+v)
		}
	}
	return out
}

// Without returns env with every key variable removed.
func Without(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !isKeyVar(kv) {
			out = append(out, kv)
		}
	}
	return out
}

func isKeyVar(kv string) bool {
	for _, name := range Names {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}
