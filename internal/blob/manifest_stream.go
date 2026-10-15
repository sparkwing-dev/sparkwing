package blob

import (
	"encoding/json"
	"errors"
	"io"
)

func verifyManifestStream(raw io.Reader, object Blob, members []Member) error {
	budget := int64(128)
	paths := make(map[string]bool, len(members))
	for _, member := range members {
		if member.Team != object.Team || member.ManifestDigest != object.Digest || member.Size < 0 || !validDigest(member.Digest) || paths[member.Path] {
			return ErrIntegrity
		}
		paths[member.Path] = true
		// safety: JSON escapes use at most six bytes per input byte; the remaining schema and whitespace are fixed per entry.
		budget += 512 + 6*(int64(len(member.Path))+int64(len(member.Digest)))
	}
	reader := &io.LimitedReader{R: &manifestJSONReader{reader: raw}, N: budget}
	i := 0
	err := walkManifestStream(reader, func(entry ManifestEntry) error {
		if i == len(members) {
			return ErrIntegrity
		}
		member := members[i]
		if entry.Path != member.Path || entry.Digest != member.Digest || entry.Size != member.Size || entry.Mode != member.Mode {
			return ErrIntegrity
		}
		i++
		return nil
	})
	if err != nil || i != len(members) || reader.N == 0 {
		return ErrIntegrity
	}
	return nil
}

func walkManifestStream(raw io.Reader, visit func(ManifestEntry) error) error {
	decoder := json.NewDecoder(raw)
	decoder.DisallowUnknownFields()
	if err := manifestToken(decoder, json.Delim('{')); err != nil {
		return err
	}
	if decoder.More() {
		if err := manifestToken(decoder, "entries"); err != nil {
			return err
		}
		if err := walkManifestEntries(decoder, visit); err != nil {
			return err
		}
	}
	if err := manifestToken(decoder, json.Delim('}')); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrIntegrity
	}
	return nil
}

func walkManifestEntries(decoder *json.Decoder, visit func(ManifestEntry) error) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return nil
	}
	if token != json.Delim('[') {
		return ErrIntegrity
	}
	for decoder.More() {
		var entry ManifestEntry
		if err := decoder.Decode(&entry); err != nil {
			return err
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
	return manifestToken(decoder, json.Delim(']'))
}

func manifestToken(decoder *json.Decoder, want any) error {
	token, err := decoder.Token()
	if err != nil || token != want {
		return ErrIntegrity
	}
	return nil
}

type manifestJSONReader struct {
	reader  io.Reader
	quoted  bool
	escaped bool
	space   bool
}

func (r *manifestJSONReader) Read(p []byte) (int, error) {
	for {
		n, err := r.reader.Read(p)
		out := 0
		for _, c := range p[:n] {
			if r.quoted {
				if r.escaped {
					r.escaped = false
				} else if c == '\\' {
					r.escaped = true
				} else if c == '"' {
					r.quoted = false
				}
			} else if c == '"' {
				r.quoted = true
			} else if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
				if r.space {
					continue
				}
				c = ' '
				r.space = true
				p[out] = c
				out++
				continue
			}
			r.space = false
			p[out] = c
			out++
		}
		if out != 0 || err != nil || len(p) == 0 {
			return out, err
		}
	}
}
