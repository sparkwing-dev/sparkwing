package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type queuePriorityFake struct {
	team       store.Team
	calls      int
	authorized capability.Authorized
	input      controllerwire.QueuePriorityWrite
	group      string
	err        error
}

func (f *queuePriorityFake) Team() store.Team { return f.team }
func (f *queuePriorityFake) SetAuthorizedGroupPriority(_ context.Context, a capability.Authorized, group string, input controllerwire.QueuePriorityWrite) (controllerwire.QueuePriorityResult, error) {
	f.calls++
	f.authorized, f.input, f.group = a, input, group
	return controllerwire.QueuePriorityResult{RunID: "run", Group: group, SetPriorityAck: wingwire.SetPriorityAck{Found: true, Priority: *input.Priority}}, f.err
}

func TestQueuePriorityHTTPPreservesTenantAuthorityProperty(t *testing.T) {
	engine, _ := engineRuntimeStore(t)
	st := engine.Store
	engineSQL(t, st, `INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('member','member@example.test',1,1)`)
	engineSQL(t, st, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('a','member','editor',1)`)
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s := NewDeployment(st, deploymenttest.Team(t), slog.New(slog.DiscardHandler)).WithSecretsCipher(cipher)
	raw, csrf, _, err := st.CreateAccountSession(t.Context(), store.Account{ID: "member"}, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	fake := &queuePriorityFake{team: "a"}
	property := func(input []byte, phase uint8) bool {
		engineSQL(t, st, `UPDATE memberships SET role='editor' WHERE account_id='member'`)
		fake.calls, fake.err, fake.team = 0, nil, "a"
		s.WithQueuePriorityWriter(func(_ context.Context, team store.Team) (QueuePriorityWriter, error) {
			if team != "a" {
				return nil, missingEndpointPolicy()
			}
			return fake, nil
		})
		body := `{"run_id":"1","priority":0}`
		want := 200
		switch phase % 9 {
		case 1:
			s.resolveQueuePriorityWriter = nil
			want = 500
		case 2:
			fake.team = "b"
			want = 500
		case 3:
			body = `{"run_id":"1","priority":0,"mode":"front"}`
			want = 400
		case 4:
			want = 400
		case 5:
			fake.err = &apierr.Error{Code: apierr.Conflict, Message: "queue changed"}
			want = 409
		case 6:
			engineSQL(t, st, `UPDATE memberships SET role='reader' WHERE account_id='member'`)
			want = 403
		case 7:
			want = 403
		case 8:
			want = 401
		}
		r := httptest.NewRequest("PUT", "https://controller.test/api/v1/queue/groups/deploy/priority?team=b&actor=forged", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://controller.test")
		sum := sha256.Sum256(input)
		key := hex.EncodeToString(sum[:])
		if phase%9 != 4 {
			r.Header.Set("Idempotency-Key", key)
		}
		if phase%9 != 8 {
			r.AddCookie(&http.Cookie{Name: cookieName(sessionCookieName, s.cookiesSecure()), Value: raw})
			r.AddCookie(&http.Cookie{Name: cookieName(csrfCookieName, s.cookiesSecure()), Value: csrf})
		}
		if phase%9 != 7 {
			r.Header.Set(csrfHeaderName, csrf)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		called := phase%9 == 0 || phase%9 == 5
		if w.Code != want || (fake.calls == 1) != called {
			t.Logf("phase %d: %d %s calls %d", phase%9, w.Code, w.Body, fake.calls)
			return false
		}
		if called {
			return fake.authorized.Target.Team == "a" && fake.authorized.Principal.Capability.Subject.ID == "member" && fake.group == "deploy" && fake.input.IdempotencyKey == key && fake.input.Priority != nil && *fake.input.Priority == 0 && fake.input.RunID == "1"
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}
