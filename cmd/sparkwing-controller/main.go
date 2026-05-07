// Command sparkwing-controller is the controller pod's entry point:
// an HTTP service fronting the run/node/event/cache state store.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator"
	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/v2/otelutil"
	"github.com/sparkwing-dev/sparkwing/v2/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/v2/secrets"
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
	_ = fs.Parse(args)

	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("open state db: %w", err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-controller"})
	defer tel.Shutdown(context.Background())

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
		WithSecretsCipher(cipher)
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
		// Tolerate both raw 32 bytes and a base64-encoded blob in the
		// file -- some operators prefer one or the other.
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
