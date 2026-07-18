// Command sparkwing-controller is the controller pod's entry point:
// an HTTP service fronting the run/node/event/cache state store.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	flag "github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sparkwing-controller:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sparkwing-controller", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:4344", "bind address")
	poolEnabled := fs.Bool("pool", false,
		"enable the warm-PVC pool (requires in-cluster K8s access)")
	poolNamespace := fs.String("pool-namespace", os.Getenv("POD_NAMESPACE"),
		"namespace the pool manages (default: POD_NAMESPACE)")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"),
		"kubeconfig path when --pool is set (empty = in-cluster)")
	secretsKeyFile := fs.String("secrets-key-file", "",
		"path to a file containing 32 raw bytes for secret encryption (alternative to SPARKWING_SECRETS_KEY)")
	cachePodURL := fs.String("cache-pod-url", os.Getenv("CACHE_POD_URL"),
		"externally-reachable URL of the sparkwing-cache pod (gitcache + artifact store). "+
			"Announced via GET /api/v1/services so operator CLIs can discover it without "+
			"hardcoding it in profiles.yaml. Empty disables the announcement.")
	cacheURL := fs.String("cache-url", os.Getenv("SPARKWING_CACHE_URL"),
		"controller-reachable sparkwing-cache URL for gitcache proxy routes")
	requireAuth := fs.Bool("require-auth", envTruthy("SPARKWING_REQUIRE_AUTH"),
		"refuse to start when the tokens table is empty, guarding against "+
			"accidentally deploying an open controller. Leave unset for "+
			"first-run bootstrap (minting the first token needs an open "+
			"controller) and for laptop-local use.")
	_ = fs.Parse(args)

	emitStartupProvenance(os.Stderr)

	p, err := paths.DefaultPaths()
	if err != nil {
		return err
	}
	if err := p.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		return mapStoreOpenError(err)
	}
	defer func() { _ = st.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-controller"})
	defer func() { _ = tel.Shutdown(context.Background()) }()

	cipher, cerr := loadSecretsCipher(*secretsKeyFile)
	if cerr != nil {
		return fmt.Errorf("load secrets key: %w", cerr)
	}
	if cipher == nil {
		fmt.Fprintln(os.Stderr,
			"sparkwing-controller: WARNING: no secrets key configured "+
				"(SPARKWING_SECRETS_KEY / --secrets-key-file unset); "+
				"secret values will be stored at rest as plaintext")
	}

	srv := controller.New(st, nil).
		EnableAuthFromStore().
		WithGitHubWebhookSecret(os.Getenv("GITHUB_WEBHOOK_SECRET")).
		WithCachePodURL(*cachePodURL).
		WithCacheURL(*cacheURL)
	// safety: a typed-nil *secrets.Cipher satisfies the interface and would register as non-nil at the handler's seam.
	if cipher != nil {
		srv = srv.WithSecretsCipher(cipher)
	}
	if *requireAuth && !srv.AuthEnabled() {
		return fmt.Errorf("--require-auth (SPARKWING_REQUIRE_AUTH) is set but " +
			"the tokens table is empty; mint an admin token with the controller " +
			"started unauthenticated, then restart with --require-auth")
	}
	if *poolEnabled {
		if *poolNamespace == "" {
			return fmt.Errorf("--pool requires --pool-namespace (or POD_NAMESPACE)")
		}
		kcli, kerr := kubeClient(*kubeconfig)
		if kerr != nil {
			return fmt.Errorf("pool: %w", kerr)
		}
		srv.AttachPool(controller.PoolConfig{
			Client:    kcli,
			Namespace: *poolNamespace,
		})
		checkStorageClasses(ctx, kcli, *poolNamespace)
	}
	return controller.ServeWith(ctx, srv, *addr)
}

// checkStorageClasses warns at startup when none of the controller's
// PVCs reference a StorageClass and the cluster has no default class.
// On such clusters PVCs sit Pending forever with no clear error
// . Best-effort: a missing RBAC bit downgrades to debug since
// the warning is informational, not load-bearing.
func checkStorageClasses(ctx context.Context, kcli kubernetes.Interface, namespace string) {
	pvcs, err := kcli.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr,
			"sparkwing-controller: storage check skipped: list PVCs:", err)
		return
	}
	for _, pvc := range pvcs.Items {
		if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
			return
		}
	}
	classes, err := kcli.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr,
			"sparkwing-controller: storage check skipped: list StorageClasses:", err)
		return
	}
	const defaultAnnotation = "storageclass.kubernetes.io/is-default-class"
	for _, sc := range classes.Items {
		if sc.Annotations[defaultAnnotation] == "true" {
			return
		}
	}
	fmt.Fprintln(os.Stderr,
		"sparkwing-controller: WARNING: no PVC declares storageClassName "+
			"and the cluster has no default StorageClass; PVCs will hang "+
			"Pending. Set storageClassName on the PVCs (helm: "+
			"--set storage.className=<class>) or mark a StorageClass "+
			"default with storageclass.kubernetes.io/is-default-class=true.")
}

// envTruthy reports whether an environment variable is set to a
// recognized affirmative value. Unset, empty, and negative spellings
// (0, false, no, off) all read as false.
func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// loadSecretsCipher resolves the controller's secret-encryption key
// from (in order) SPARKWING_SECRETS_KEY env var, then --secrets-key-
// file. Returns nil cipher + nil error when neither is set so the
// caller can log a warning and run unencrypted.
func loadSecretsCipher(filePath string) (*secrets.Cipher, error) {
	if v := os.Getenv("SPARKWING_SECRETS_KEY"); v != "" {
		key, err := secrets.DecodeKey(v)
		if err != nil {
			return nil, fmt.Errorf("SPARKWING_SECRETS_KEY: %w", err)
		}
		return secrets.NewCipher(key)
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filePath, err)
		}
		if len(data) == secrets.KeySize {
			return secrets.NewCipher(data)
		}
		decoded, derr := secrets.DecodeKey(string(data))
		if derr != nil {
			return nil, fmt.Errorf("%s: %w", filePath, derr)
		}
		return secrets.NewCipher(decoded)
	}
	return nil, nil
}

// kubeClient builds a kubernetes.Interface for --pool. In-cluster
// config is used when kubeconfig is empty, matching the typical pod
// shape. Duplicated from the pre-split sparkwing binary; can move to
// pkg/kubeutil if another binary needs it.
func kubeClient(kubeconfig string) (kubernetes.Interface, error) {
	var rc *rest.Config
	var err error
	if kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kube config: %w", err)
	}
	return kubernetes.NewForConfig(rc)
}
