package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
)

// MergeSDKEntries retains immutable SDK publication entries without granting artifact-controlled revocations.
func MergeSDKEntries(index VersionIndex, body []byte) (VersionIndex, error) {
	var entries []SDKRelease
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return index, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return index, errors.New("SDK entries contain trailing data")
	}
	for _, entry := range entries {
		if entry.Revoked != nil {
			return index, errors.New("release artifacts cannot set SDK revocations")
		}
		found := false
		for _, previous := range index.SDKs {
			if previous.Language != entry.Language || previous.Version != entry.Version {
				continue
			}
			found = true
			comparable := previous
			comparable.Revoked = nil
			if !reflect.DeepEqual(comparable, entry) {
				return index, fmt.Errorf("SDK %s %s is immutable", entry.Language, entry.Version)
			}
		}
		if !found {
			index.SDKs = append(index.SDKs, entry)
		}
	}
	return index, nil
}
