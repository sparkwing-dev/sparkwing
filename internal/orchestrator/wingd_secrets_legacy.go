package orchestrator

// hack: the import of the local dotenv secret files, served by the daemon. A
// later release deletes this file with internal/localsecrets/legacy_migrate.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// APISecretsImportRoute imports the dotenv files local secrets lived in before
// the runs store held them. Only the daemon serves it, over its socket; the
// caller names the files it resolved, because its config directory is the one
// the operator means.
const APISecretsImportRoute = "POST /api/v1/local/secrets/import"

const maxSecretsImportBody = 8 << 10

type secretsImportResponse struct {
	Files []localsecrets.ImportedFile `json:"files"`
}

func (a *wingdAPI) importLegacyOnOpen(ctx context.Context, rw *store.Store, cipher *localsecrets.Cipher) {
	files, warning := localsecrets.FindLegacyFiles()
	if warning != "" {
		a.logger.Warn(warning)
	}
	if files.Empty() {
		return
	}
	imported, err := localsecrets.ImportLegacy(ctx, rw, cipher, files, time.Now())
	if err != nil {
		a.logger.Error("import local dotenv secrets", "err", err)
		return
	}
	for _, f := range imported {
		a.logger.Info("imported local dotenv secrets", "path", f.Path, "imported", f.Imported, "conflicts", strings.Join(f.Conflicts, ","))
	}
}

func (a *wingdAPI) importLegacySecrets(w http.ResponseWriter, r *http.Request) {
	var files localsecrets.LegacyFiles
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSecretsImportBody)).Decode(&files); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	rw, ro, err := a.runs.Create(r.Context())
	if err != nil {
		writeAPIUnavailable(w, err)
		return
	}
	a.handlerFor(r.Context(), rw, ro)
	if err := a.secrets.MissingKey(r.Context(), rw); err != nil {
		writeImportError(w, err)
		return
	}
	now := time.Now()
	if _, err := localsecrets.ImportLegacy(r.Context(), rw, a.secrets.For(rw), files, now); err != nil {
		writeImportError(w, err)
		return
	}
	report, err := localsecrets.TakeLegacyReport(r.Context(), rw, files, now)
	if err != nil {
		writeImportError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(secretsImportResponse{Files: report}); err != nil {
		a.logger.Warn("write the secrets import answer", "err", err)
	}
}

func writeImportError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	if werr := json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}); werr != nil {
		fmt.Fprintf(os.Stderr, "wingd: write the secrets import refusal: %v\n", werr)
	}
}

// ImportLegacySecretsOverSocket asks the daemon behind httpClient to import
// the dotenv files this process finds, and prints each file's one-time
// notice to stderr. A daemon too old to serve the import is told nothing.
func ImportLegacySecretsOverSocket(ctx context.Context, httpClient *http.Client) error {
	return importLegacySecretsOverSocket(ctx, httpClient, os.Stderr)
}

func importLegacySecretsOverSocket(ctx context.Context, httpClient *http.Client, out io.Writer) error {
	files, warning := localsecrets.FindLegacyFiles()
	if warning != "" {
		fmt.Fprintln(out, warning)
	}
	if files.Empty() {
		return nil
	}
	body, err := json.Marshal(files)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, HostedAPIBaseURL+"/api/v1/local/secrets/import", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("import local secrets: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if rerr == nil && json.Unmarshal(raw, &failure) == nil && failure.Error != "" {
			return fmt.Errorf("import local secrets: %s", failure.Error)
		}
		return fmt.Errorf("import local secrets: daemon answered %s", resp.Status)
	}
	var answer secretsImportResponse
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return fmt.Errorf("import local secrets: %w", err)
	}
	for _, f := range answer.Files {
		fmt.Fprintln(out, f.Notice())
	}
	return nil
}

// ImportLegacySecretsInProcess imports the dotenv files this process finds
// into the runs store at dbPath, for a run with no daemon to ask, and prints
// each file's one-time notice to stderr.
func ImportLegacySecretsInProcess(ctx context.Context, dbPath string) (err error) {
	files, warning := localsecrets.FindLegacyFiles()
	if warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	if files.Empty() {
		return nil
	}
	ring, err := localsecrets.LoadKeyring(false)
	if err != nil {
		return err
	}
	st, err := storeOpen(dbPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", dbPath, cerr)
		}
	}()
	now := time.Now()
	if _, err := localsecrets.ImportLegacy(ctx, st, ring.For(st), files, now); err != nil {
		return err
	}
	report, err := localsecrets.TakeLegacyReport(ctx, st, files, now)
	if err != nil {
		return err
	}
	for _, f := range report {
		fmt.Fprintln(os.Stderr, f.Notice())
	}
	return nil
}
