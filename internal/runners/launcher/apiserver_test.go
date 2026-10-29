package launcher

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func apiServer(t *testing.T) *rest.Config {
	t.Helper()
	return apiServerAuthorizing(t, "AlwaysAllow")
}

// apiServerAuthorizing starts the API server with the given authorization mode,
// so RBAC-limited clients can be checked against real authorization.
func apiServerAuthorizing(t *testing.T, authorization string) *rest.Config {
	t.Helper()
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS unset; run setup-envtest use 1.37.0 -p path to exercise admission against a real API server")
	}
	dir := t.TempDir()
	ports := freePorts(t, 3)
	client, peer, secure := ports[0], ports[1], ports[2]
	etcdURL := "http://127.0.0.1:" + client
	etcdExit := start(t, filepath.Join(assets, "etcd"), "--data-dir="+filepath.Join(dir, "etcd"), "--listen-client-urls="+etcdURL, "--advertise-client-urls="+etcdURL, "--listen-peer-urls=http://127.0.0.1:"+peer, "--initial-advertise-peer-urls=http://127.0.0.1:"+peer, "--initial-cluster=default=http://127.0.0.1:"+peer, "--unsafe-no-fsync")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyFile, tokens := filepath.Join(dir, "sa.pem"), filepath.Join(dir, "tokens.csv")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	token := rand.Text()
	if err := os.WriteFile(tokens, []byte(token+`,admin,admin,"system:masters"`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	apiExit := start(t, filepath.Join(assets, "kube-apiserver"), "--etcd-servers="+etcdURL, "--cert-dir="+filepath.Join(dir, "certs"), "--bind-address=127.0.0.1", "--secure-port="+secure,
		"--service-account-issuer=https://kubernetes.default.svc", "--service-account-key-file="+keyFile, "--service-account-signing-key-file="+keyFile,
		"--token-auth-file="+tokens, "--authorization-mode="+authorization, "--allow-privileged=true", "--service-cluster-ip-range=10.0.0.0/24")
	cfg := &rest.Config{Host: "https://127.0.0.1:" + secure, BearerToken: token, TLSClientConfig: rest.TLSClientConfig{Insecure: true}, QPS: -1}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		select {
		case err := <-etcdExit:
			return false, fmt.Errorf("etcd exited: %w", err)
		case err := <-apiExit:
			return false, fmt.Errorf("kube-apiserver exited: %w", err)
		default:
		}
		body, err := kube.Discovery().RESTClient().Get().AbsPath("/readyz").DoRaw(ctx)
		return err == nil && string(body) == "ok", nil
	})
	if err != nil {
		t.Fatalf("kube-apiserver not ready: %v", err)
	}
	return cfg
}

func TestStartReapsAnAlreadyObservedProcess(t *testing.T) {
	t.Run("exited before cleanup", func(t *testing.T) {
		exited := start(t, os.Args[0], "-test.run=^$")
		if err := <-exited; err != nil {
			t.Fatal(err)
		}
	})
}

func start(t *testing.T, binary string, args ...string) <-chan error {
	t.Helper()
	cmd := exec.Command(binary, args...)
	log, err := os.Create(filepath.Join(t.TempDir(), filepath.Base(binary)+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Error(err)
		}
		<-exited
		if t.Failed() {
			out, _ := os.ReadFile(log.Name())
			t.Logf("%s output:\n%s", filepath.Base(binary), tail(out))
		}
		log.Close()
	})
	return exited
}

func tail(out []byte) []byte {
	if len(out) > 4096 {
		return out[len(out)-4096:]
	}
	return out
}

// safety: every listener stays open until all are chosen, so no two servers get one port.
func freePorts(t *testing.T, n int) []string {
	t.Helper()
	ports := make([]string, n)
	for i := range ports {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		ports[i] = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	}
	return ports
}

func impersonate(t *testing.T, cfg *rest.Config, user string) kubernetes.Interface {
	t.Helper()
	as := rest.CopyConfig(cfg)
	as.Impersonate = rest.ImpersonationConfig{UserName: user}
	if strings.HasPrefix(user, "system:serviceaccount:") {
		parts := strings.Split(user, ":")
		as.Impersonate.Groups = []string{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:" + parts[2]}
	}
	kube, err := kubernetes.NewForConfig(as)
	if err != nil {
		t.Fatal(fmt.Errorf("impersonate %s: %w", user, err))
	}
	return kube
}
