package client

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

type listResponse struct {
	NextCursor string `json:"next_cursor,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	fields     map[string]json.RawMessage
}

func (p *listResponse) UnmarshalJSON(body []byte) error {
	type metadata listResponse
	if err := json.Unmarshal(body, (*metadata)(p)); err != nil {
		return err
	}
	return json.Unmarshal(body, &p.fields)
}

// requestList drains a collection while preserving its first page's metadata.
func (c *engineClient) requestList(ctx context.Context, pattern, path string, result any, fields ...string) error {
	parsed, err := url.Parse(path)
	if err != nil {
		return err
	}
	var combined map[string]json.RawMessage
	rows := make(map[string][]json.RawMessage, len(fields))
	for _, field := range fields {
		rows[field] = []json.RawMessage{}
	}
	seen := map[string]bool{}
	for {
		var response listResponse
		if err := c.request(ctx, "GET", pattern, parsed.String(), "", nil, &response); err != nil {
			return err
		}
		page := response.fields
		if err := requiredResponseFields(page, reflect.TypeOf(result).Elem()); err != nil {
			return err
		}
		if combined == nil {
			combined = page
		}
		for _, field := range fields {
			var entries []json.RawMessage
			if raw := page[field]; raw != nil {
				if err := json.Unmarshal(raw, &entries); err != nil {
					return err
				}
			}
			rows[field] = append(rows[field], entries...)
		}
		next := response.NextCursor
		if response.Truncated != (next != "") || next != "" && seen[next] {
			return &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned inconsistent collection pagination"}
		}
		if next == "" {
			break
		}
		seen[next] = true
		query := parsed.Query()
		query.Set("cursor", next)
		parsed.RawQuery = query.Encode()
	}
	for _, field := range fields {
		combined[field], err = json.Marshal(rows[field])
		if err != nil {
			return err
		}
	}
	delete(combined, "next_cursor")
	delete(combined, "truncated")
	body, err := json.Marshal(combined)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, result)
}
