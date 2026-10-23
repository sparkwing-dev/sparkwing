//go:build qatrust

// Command qaindex writes and signs the QA pre-release version index. It builds
// only with the qatrust tag and signs only with the QA key, so it cannot
// produce a production index.
//
//	SPARKWING_RELEASE_SIGNING_KEY=<QA seed> go run -tags qatrust ./bin/qaindex \
//	  --version v0.68.0-qa.1 --previous <dir with the current index> --out dist
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

var qaVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-qa\.[1-9][0-9]*$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "qaindex:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("qaindex", flag.ContinueOnError)
	version := flags.String("version", "", "QA pre-release version, vX.Y.Z-qa.N")
	previous := flags.String("previous", "", "directory holding the published versions.json and versions.json.sig; empty for the first QA release")
	out := flags.String("out", "", "directory to write versions.json and versions.json.sig")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !qaVersion.MatchString(*version) || *out == "" {
		return errors.New("--version vX.Y.Z-qa.N and --out are required")
	}
	key, err := releaseauth.PrivateKey(os.Getenv("SPARKWING_RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	if base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)) != releaseauth.QAPublicKey {
		return errors.New("signing key is not the QA release key")
	}
	keys, err := releaseauth.PublicKeys()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	index := engine.VersionIndex{Format: engine.VersionIndexFormat}
	if *previous != "" {
		if index, err = readPrevious(*previous, now, keys); err != nil {
			return err
		}
	}
	if index, err = nextIndex(index, *version, now); err != nil {
		return err
	}
	body, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	signature, err := releaseauth.Sign(key, body)
	if err != nil {
		return err
	}
	if _, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: signature}, now, keys); err != nil {
		return fmt.Errorf("refusing an index the client would reject: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*out, "versions.json"), body, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*out, "versions.json.sig"), signature, 0o644)
}

func readPrevious(dir string, now time.Time, keys []ed25519.PublicKey) (engine.VersionIndex, error) {
	body, err := os.ReadFile(filepath.Join(dir, "versions.json"))
	if err != nil {
		return engine.VersionIndex{}, err
	}
	signature, err := os.ReadFile(filepath.Join(dir, "versions.json.sig"))
	if err != nil {
		return engine.VersionIndex{}, err
	}
	verified, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: signature}, now, keys)
	if err != nil {
		return engine.VersionIndex{}, fmt.Errorf("published QA index: %w", err)
	}
	return verified.Policy(), nil
}

func nextIndex(index engine.VersionIndex, version string, now time.Time) (engine.VersionIndex, error) {
	for _, record := range index.Versions {
		if record.Version == version {
			return index, fmt.Errorf("%s is already published", version)
		}
	}
	index.PublishedAt = now
	if index.Latest == "" || semver.Compare(version, index.Latest) > 0 {
		index.Latest = version
	}
	index.Versions = append(index.Versions, engineid.VersionInfo{Version: version, PublishedAt: now, SupportUntil: now.AddDate(0, limits.ControllerCompatibilityMonths, 0)})
	return index, nil
}
