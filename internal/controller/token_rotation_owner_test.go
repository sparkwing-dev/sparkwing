package controller_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type mintedToken struct {
	Capability string `json:"capability"`
	Prefix     string `json:"prefix"`
}

func (f *tenancyFixture) mint(t *testing.T, path, auth string, body any) mintedToken {
	t.Helper()
	code, raw := f.do("POST", path, auth, body)
	var out mintedToken
	if code != http.StatusCreated || json.Unmarshal([]byte(raw), &out) != nil || out.Capability == "" || out.Prefix == "" {
		t.Fatalf("POST %s = %d: %s", path, code, raw)
	}
	return out
}

func (f *tenancyFixture) rotate(t *testing.T, prefix, auth string) mintedToken {
	t.Helper()
	code, raw := f.do("POST", "/api/v1/tokens/"+prefix+"/rotate", auth, map[string]any{"grace_secs": 0})
	var out struct {
		Capability string      `json:"capability"`
		New        mintedToken `json:"new"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(raw), &out) != nil || out.Capability == "" || out.New.Prefix == "" {
		t.Fatalf("rotate %s = %d: %s", prefix, code, raw)
	}
	return mintedToken{Capability: out.Capability, Prefix: out.New.Prefix}
}

func TestTokenRotationBelongsToTheOwnerAndRevocationToTheAdmin(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		repos := []string{"github.com/*/*"}
		cli := f.mint(t, "/api/v1/tokens", f.editorA, map[string]any{"grants": []string{"runs.read"}})
		laptop := f.mint(t, "/api/v1/runners", f.editorA, map[string]any{"name": "dave-laptop", "repos": repos, "personal": true})
		shared := f.mint(t, "/api/v1/runners", f.ownerA, map[string]any{"name": "shared-a", "repos": repos})

		for _, other := range []mintedToken{cli, laptop} {
			code, body := f.do("POST", "/api/v1/tokens/"+other.Prefix+"/rotate", f.ownerA, map[string]any{"grace_secs": 0})
			if code != http.StatusForbidden || strings.Contains(body, `"capability"`) {
				t.Fatalf("admin rotated a member's token %s = %d: %s", other.Prefix, code, body)
			}
		}
		if code, body := f.do("GET", "/api/v1/runs/"+f.runA, "Bearer "+cli.Capability, nil); code != http.StatusOK {
			t.Fatalf("refused rotation disturbed the member token = %d: %s", code, body)
		}

		own := f.rotate(t, cli.Prefix, f.editorA)
		if code, _ := f.do("GET", "/api/v1/runs/"+f.runA, "Bearer "+cli.Capability, nil); code != http.StatusUnauthorized {
			t.Fatalf("rotated predecessor still works = %d", code)
		}
		f.rotate(t, laptop.Prefix, f.editorA)
		f.rotate(t, shared.Prefix, f.ownerA)

		if code, body := f.do("DELETE", "/api/v1/tokens/"+own.Prefix, f.ownerA, nil); code/100 != 2 {
			t.Fatalf("admin revoke of a member's token = %d: %s", code, body)
		}
		if code, _ := f.do("GET", "/api/v1/runs/"+f.runA, "Bearer "+own.Capability, nil); code != http.StatusUnauthorized {
			t.Fatalf("revoked token still works = %d", code)
		}
	})
}
