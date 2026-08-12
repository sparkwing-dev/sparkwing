package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("verify-release", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "release asset directory")
	verify := fs.Bool("verify", false, "verify existing signatures")
	public := fs.Bool("public-key", false, "print the public key derived from the signing seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	privateKey, err := releaseauth.PrivateKey(os.Getenv("SPARKWING_RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if !releaseauth.TrustedPublicKey(base64.StdEncoding.EncodeToString(publicKey)) {
		return errors.New("release signing key is not trusted by shipped updaters")
	}
	if *public {
		fmt.Println(base64.StdEncoding.EncodeToString(publicKey))
		return nil
	}
	return process(*dist, privateKey, publicKey, *verify)
}

func process(dist string, privateKey ed25519.PrivateKey, publicKey ed25519.PublicKey, verify bool) error {
	manifestPath := filepath.Join(dist, "SHA256SUMS")
	assets, err := filepath.Glob(filepath.Join(dist, "sparkwing-*"))
	if err != nil {
		return err
	}
	assets = unsignedAssets(assets)
	if len(assets) == 0 {
		return errors.New("no Sparkwing release assets")
	}
	sort.Strings(assets)
	paths := append([]string{manifestPath}, assets...)
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		signaturePath := path + ".sig"
		if verify {
			signature, err := os.ReadFile(signaturePath)
			if err != nil {
				return err
			}
			if err := releaseauth.Verify(publicKey, body, signature); err != nil {
				return fmt.Errorf("verify %s: %w", filepath.Base(path), err)
			}
			continue
		}
		signature, err := releaseauth.Sign(privateKey, body)
		if err != nil {
			return err
		}
		if err := os.WriteFile(signaturePath, signature, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func unsignedAssets(paths []string) []string {
	result := paths[:0]
	for _, path := range paths {
		if !strings.HasSuffix(path, ".sig") {
			result = append(result, path)
		}
	}
	return result
}
