package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

type daemonEventCursor struct {
	Version   int       `json:"version"`
	Query     string    `json:"query"`
	Cutoff    time.Time `json:"cutoff"`
	Remaining int       `json:"remaining"`
	Prefix    string    `json:"prefix"`
}

type daemonEventSelection struct {
	records []journal.Record
	next    string
}

func invalidDaemonEventCursor() error {
	return &clisurface.UsageError{Path: "sparkwing daemon events", Message: "invalid --cursor or changed filters; repeat the same filters or restart without --cursor"}
}

func decodeDaemonEventCursor(value string) (daemonEventCursor, error) {
	var cursor daemonEventCursor
	if value == "" {
		return cursor, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return cursor, invalidDaemonEventCursor()
	}
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return cursor, invalidDaemonEventCursor()
	}
	canonical, err := json.Marshal(cursor)
	query, queryErr := hex.DecodeString(cursor.Query)
	prefix, prefixErr := hex.DecodeString(cursor.Prefix)
	if err != nil || !bytes.Equal(raw, canonical) || cursor.Version != 1 || cursor.Remaining < 1 || queryErr != nil || prefixErr != nil || len(query) != sha256.Size || len(prefix) != sha256.Size {
		return cursor, invalidDaemonEventCursor()
	}
	return cursor, nil
}

func daemonEventDigest(value any) (string, error) {
	hash := sha256.New()
	if err := json.NewEncoder(hash).Encode(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func selectDaemonEventPage(records []journal.Record, dir string, query daemonEventQuery, limit int, cursor daemonEventCursor, now time.Time) (daemonEventSelection, error) {
	page := daemonEventSelection{}
	fingerprint, err := daemonEventDigest(struct {
		Directory string
		Query     daemonEventQuery
	}{dir, query})
	if err != nil {
		return page, err
	}
	if cursor.Version == 0 {
		cursor = daemonEventCursor{Version: 1, Query: fingerprint}
		if query.Since > 0 {
			cursor.Cutoff = now.Add(-query.Since).UTC()
		}
	} else if cursor.Query != fingerprint {
		return page, invalidDaemonEventCursor()
	}
	selected := make([]journal.Record, 0, len(records))
	for _, record := range records {
		if query.Run != "" && record.RunID != query.Run && record.DisplayRunID != query.Run {
			continue
		}
		if !cursor.Cutoff.IsZero() && record.TS.Before(cursor.Cutoff) {
			continue
		}
		if query.Incarnation != 0 && record.Incarnation != query.Incarnation {
			continue
		}
		if len(query.Kinds) > 0 && !slices.Contains(query.Kinds, record.Kind) {
			continue
		}
		selected = append(selected, record)
	}
	if cursor.Remaining > 0 {
		if len(selected) < cursor.Remaining {
			return page, staleDaemonEventCursor()
		}
		selected = selected[:cursor.Remaining]
		digest, err := daemonEventDigest(selected)
		if err != nil {
			return page, err
		}
		if digest != cursor.Prefix {
			return page, staleDaemonEventCursor()
		}
	}
	start := max(0, len(selected)-limit)
	page.records = selected[start:]
	if start > 0 {
		cursor.Remaining = start
		cursor.Prefix, err = daemonEventDigest(selected[:start])
		if err != nil {
			return page, err
		}
		raw, err := json.Marshal(cursor)
		if err != nil {
			return page, err
		}
		page.next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
