// Package httpapi serves the billing endpoints.
package httpapi

import (
	"encoding/json"
	"net/http"

	"example.com/paygate/internal/billing"
	"example.com/paygate/internal/store"
)

// Handler serves account and invoice routes against a Store.
type Handler struct {
	Store *store.Store
}

// Routes returns the mux with every endpoint registered.
func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /accounts/{id}", h.getAccount)
	mux.HandleFunc("POST /invoices/total", h.invoiceTotal)
	return mux
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	acct, err := h.Store.Get(r.PathValue("id"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, acct)
}

func (h *Handler) invoiceTotal(w http.ResponseWriter, r *http.Request) {
	var lines []billing.Line
	if err := json.NewDecoder(r.Body).Decode(&lines); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	total, err := billing.Total(lines)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"cents": total})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
