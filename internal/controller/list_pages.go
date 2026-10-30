package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

const (
	defaultCollectionPage = 50
	maxCollectionPage     = 200
)

type collectionCursor struct {
	Scope string `json:"scope"`
	After string `json:"after"`
}

type collectionPage struct {
	NextCursor string `json:"next_cursor"`
	Truncated  bool   `json:"truncated"`
}

func collectionSelection(r *http.Request, a capability.Authorized) (int, collectionCursor, error) {
	query := r.URL.Query()
	limit := defaultCollectionPage
	if raw, exists := query["limit"]; exists {
		n, err := strconv.Atoi(query.Get("limit"))
		if len(raw) != 1 || err != nil || n < 1 || n > maxCollectionPage {
			return 0, collectionCursor{}, invalidEngineBody(fmt.Sprintf("list page size limit is %d; requested %q; choose 1 to %d or contact support for larger pages", maxCollectionPage, query.Get("limit"), maxCollectionPage))
		}
		limit = n
	}
	raw := query.Get("cursor")
	if len(query["cursor"]) > 1 {
		return 0, collectionCursor{}, invalidEngineBody("cursor must appear once")
	}
	query.Del("cursor")
	query.Del("limit")
	body, err := json.Marshal([]string{r.URL.Path, a.Target.Team, a.Principal.Capability.Subject.ID, query.Encode()})
	if err != nil {
		return 0, collectionCursor{}, err
	}
	sum := sha256.Sum256(body)
	cursor := collectionCursor{Scope: base64.RawURLEncoding.EncodeToString(sum[:])}
	if raw != "" {
		var decoded collectionCursor
		body, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(body, &decoded) != nil || decoded.Scope != cursor.Scope || decoded.After == "" {
			return 0, collectionCursor{}, invalidEngineBody("cursor must be the next_cursor from this list with the same team and filters")
		}
		cursor = decoded
	}
	return limit, cursor, nil
}

func pageCollection[T any](r *http.Request, a capability.Authorized, rows []T, key func(T) string) ([]T, collectionPage, error) {
	limit, cursor, err := collectionSelection(r, a)
	if err != nil {
		return nil, collectionPage{}, err
	}
	slices.SortStableFunc(rows, func(a, b T) int { return strings.Compare(key(a), key(b)) })
	start := sortCollectionAfter(rows, cursor.After, key)
	end := min(start+limit, len(rows))
	page := collectionPage{Truncated: end < len(rows)}
	if page.Truncated {
		cursor.After = key(rows[end-1])
		body, err := json.Marshal(cursor)
		if err != nil {
			return nil, collectionPage{}, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(body)
	}
	return append([]T{}, rows[start:end]...), page, nil
}

func sortCollectionAfter[T any](rows []T, after string, key func(T) string) int {
	if after == "" {
		return 0
	}
	lo, hi := 0, len(rows)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if key(rows[mid]) <= after {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func writeListPage[T any](w http.ResponseWriter, r *http.Request, a capability.Authorized, field string, rows []T, key func(T) string, extra map[string]any) error {
	rows, page, err := pageCollection(r, a, rows, key)
	if err != nil {
		return err
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra[field], extra["next_cursor"], extra["truncated"] = rows, page.NextCursor, page.Truncated
	writeJSON(w, http.StatusOK, extra)
	return nil
}

func stepCollectionKey(job string, attempt int64, started *time.Time, id string) string {
	var stamp int64
	if started != nil {
		stamp = started.UnixNano()
	}
	return fmt.Sprintf("%s/%020d/%016x/%s", job, attempt, uint64(stamp)^(1<<63), id)
}
