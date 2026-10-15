package localws

import (
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/originguard"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Options configures the local HTTP handlers.
type Options struct {
	Addr, Home            string
	ReadOnly, AllowRemote bool
	AllowOrigins          []string
	Version, Instance     string
	Bundle                fs.FS
}

type handlerParts struct {
	paths      orchestrator.Paths
	store      *store.Store
	ctrl       *controller.Server
	serveToken string
}

func buildHandler(opts Options, parts handlerParts) http.Handler {
	root := http.NewServeMux()
	root.Handle("GET /api/v1/version", versionHandler(opts.Version, opts.Instance))
	root.Handle("GET /api/v1/pipelines", aggregatedPipelinesHandler())
	root.Handle("GET /api/v1/queue", queueHandler(parts.paths.Root, opts.Version))
	registerCronRoutes(root, &cronsAPI{store: parts.store, paths: parts.paths, readOnly: opts.ReadOnly})
	// safety: this API has no sign-in, so a masked value never leaves it;
	// the dashboard lists, writes and deletes rows without reading one.
	root.Handle("GET /api/v1/secrets/{name}", http.NotFoundHandler())
	root.Handle("/", parts.ctrl.Handler())

	var handler http.Handler = root
	if opts.ReadOnly {
		handler = readOnlyMiddleware(handler)
	}
	// safety: the sign-in exchange lives in the token gate, so the gate sits
	// inside the Host and Origin guard or a rebound page could trade a code.
	if parts.serveToken != "" {
		handler = requireServeToken(handler, parts.serveToken)
	}
	return web.SecurityHeaders(false,
		originguard.Guard(handler, originguard.NewPolicy(opts.Addr, opts.AllowRemote, opts.AllowOrigins)))
}

func localPaths(home string) (orchestrator.Paths, error) {
	if home != "" {
		return orchestrator.PathsAt(home), nil
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return orchestrator.Paths{}, fmt.Errorf("resolve sparkwing home: %w", err)
	}
	return paths, nil
}

func localFeatures() []string {
	return []string{
		"pipelines", "runs", "logs",
		"secrets", "approvals", "cross-pipeline-refs",
	}
}

func readOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") ||
			strings.HasPrefix(r.URL.Path, "/webhooks/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		http.Error(w, "read-only mode: writes disabled", http.StatusMethodNotAllowed)
	})
}

func writeDevEnv(root, baseURL string) error {
	body := fmt.Sprintf("SPARKWING_CONTROLLER_URL=%s\n", baseURL)
	return fssecure.WriteFile(filepath.Join(root, "dev.env"), []byte(body))
}
