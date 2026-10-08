package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type brokeredRunnerNode struct {
	store  *store.Store
	url    string
	token  string
	fence  store.NodeClaimFence
	broker *remoteExecutionBroker
	child  *client.Client
}

func newBrokeredRunnerNode(t *testing.T, configure func(*controller.Server, string) *controller.Server) brokeredRunnerNode {
	t.Helper()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	token, _, err := st.CreateToken("runner-a", store.TokenKindRunner, []string{
		controller.ScopeNodesClaim, controller.ScopeTriggersClaim, controller.ScopeRunsState,
		controller.ScopeSecretsRead, controller.ScopeLogsWrite,
	}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.CreateRun(ctx, store.Run{ID: "run-1", Pipeline: "demo", Status: "running", StartedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-1", NodeID: "parent", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-1", "parent"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(nil)
	c := controller.New(st, slog.New(slog.NewTextHandler(io.Discard, nil))).EnableAuthFromStore()
	if configure != nil {
		c = configure(c, "http://"+srv.Listener.Addr().String())
	}
	srv.Config.Handler = c.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	n, err := client.NewWithToken(srv.URL, nil, token).ClaimNode(ctx, "runner:box-a:1", nil, time.Minute, nil)
	if err != nil || n == nil || n.NodeID != "parent" {
		t.Fatalf("claim parent = %+v, %v", n, err)
	}
	fence := store.NodeClaimFence{
		HolderID: n.ClaimedBy, MembershipID: n.ClaimMembershipID,
		ReservationID: n.ReservationID, ClaimGeneration: n.ClaimGeneration,
	}
	broker, err := startRemoteExecutionBroker(srv.URL, "", token, "run-1", "parent", fence, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(broker.Close)
	return brokeredRunnerNode{
		store: st, url: srv.URL, token: token, fence: fence, broker: broker,
		child: client.NewWithToken(broker.URL(), nil, broker.capability),
	}
}

const brokerRefusal = "execution capability does not allow this route"

func TestBrokeredNodeMintsAnOIDCToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signing := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	f := newBrokeredRunnerNode(t, func(c *controller.Server, issuer string) *controller.Server {
		iss, err := oidcissuer.New(issuer, signing, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return c.WithOIDCIssuer(iss)
	})
	ctx := context.Background()
	direct, err := client.NewWithToken(f.url, nil, f.token).OIDCToken(store.WithNodeClaimFence(ctx, f.fence), "run-1", "sts.amazonaws.com")
	if err != nil || direct.Token == "" {
		t.Fatalf("direct mint = %+v, %v", direct, err)
	}
	brokered, err := f.child.OIDCToken(ctx, "run-1", "sts.amazonaws.com")
	if err != nil || brokered.Token == "" {
		t.Fatalf("brokered mint = %+v, %v", brokered, err)
	}
	if _, err := f.child.OIDCToken(ctx, "run-2", "sts.amazonaws.com"); err == nil || !strings.Contains(err.Error(), brokerRefusal) {
		t.Fatalf("mint for another run = %v, want the broker's refusal", err)
	}
}
