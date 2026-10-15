package teamblob_test

import (
	"errors"
	"fmt"
	"math/rand"
	"path"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

func TestKeysStayInTheirNamespace(t *testing.T) {
	store, err := teamblob.New(teamblob.Options{Bucket: "test", Prefix: "svc"})
	if err != nil {
		t.Fatal(err)
	}
	property := func(seed [16]byte, rel string) bool {
		team := fmt.Sprintf("t-%x", seed)
		key, err := store.Key(team, rel)
		if err != nil {
			return true
		}
		other, err := store.Key(team+"-other", rel)
		return err == nil && key != other && strings.HasPrefix(key, "svc/teams/"+team+"/") && path.Clean(key) == key && !strings.Contains(key, "\\")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(66))}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"../other", "a/../../other", "a//b", "./x", "x\\y", "", "/abs", "a/", "x\x00y"} {
		if _, err := store.Key("team", rel); !errors.Is(err, teamblob.ErrInvalidKey) {
			t.Errorf("accepted relative key %q: %v", rel, err)
		}
	}
	for _, rel := range []string{"teams/other/cas/x", "_meta/usage.json"} {
		if _, err := store.Key("", rel); !errors.Is(err, teamblob.ErrInvalidKey) {
			t.Errorf("accepted operator key %q: %v", rel, err)
		}
	}
}
