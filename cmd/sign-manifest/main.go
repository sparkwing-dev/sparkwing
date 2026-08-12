// Command sign-manifest signs a release's SHA256SUMS with an ed25519
// private key, producing a detached signature the sparkwing self-updater
// verifies with the ed25519 public key compiled into its binary
// (cmd/sparkwing/update.go). It uses the same pure-Go crypto/ed25519 as
// the updater, so the signing side stays auditable Go rather than an
// opaque external tool.
//
// Two modes:
//
//	# 1. Generate a keypair (one time, by the release owner):
//	go run ./cmd/sign-manifest -genkey
//	  -> prints the PUBLIC key  (hex)    -> paste into cmd/sparkwing/update.go
//	  -> prints the PRIVATE key (base64) -> store as the GitHub Actions
//	     secret SPARKWING_UPDATE_SIGNING_KEY
//
//	# 2. Sign a manifest (in CI, from the private key in the environment):
//	SPARKWING_UPDATE_SIGNING_KEY=<base64> \
//	  go run ./cmd/sign-manifest -in dist/SHA256SUMS -out dist/SHA256SUMS.sig
//
// The signature file holds the raw 64-byte ed25519 signature over the raw
// bytes of the input file -- exactly what ed25519.Verify(pub, sums, sig)
// consumes on the client.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
)

// signingKeyEnv names the environment variable that carries the base64
// ed25519 private key (64 bytes: seed||public) in CI.
const signingKeyEnv = "SPARKWING_UPDATE_SIGNING_KEY"

func main() {
	genkey := flag.Bool("genkey", false, "generate an ed25519 keypair and print the public (hex) and private (base64) keys")
	in := flag.String("in", "", "path to the file to sign (e.g. dist/SHA256SUMS)")
	out := flag.String("out", "", "path to write the detached signature to (e.g. dist/SHA256SUMS.sig)")
	flag.Parse()

	if *genkey {
		if err := runGenKey(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sign-manifest:", err)
			os.Exit(1)
		}
		return
	}

	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "sign-manifest: -in and -out are required (or pass -genkey)")
		flag.Usage()
		os.Exit(2)
	}

	priv, err := loadSigningKey(os.Getenv(signingKeyEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sign-manifest: %v\n", err)
		os.Exit(1)
	}
	if err := signFile(priv, *in, *out); err != nil {
		fmt.Fprintf(os.Stderr, "sign-manifest: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "wrote detached signature: %s\n", *out)
}

// runGenKey prints a fresh keypair: the public key as hex (to paste into
// the updater) and the private key as base64 (to store as the CI secret).
func runGenKey(w *os.File) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "ed25519 keypair generated.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "PUBLIC KEY (hex) -- paste into sparkwingUpdatePubKeyHex in cmd/sparkwing/update.go:")
	fmt.Fprintln(w, "  "+hex.EncodeToString(pub))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "PRIVATE KEY (base64) -- store as the GitHub Actions secret %s:\n", signingKeyEnv)
	fmt.Fprintln(w, "  "+base64.StdEncoding.EncodeToString(priv))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Keep the private key secret. Anyone holding it can sign a manifest the fleet will trust.")
	return nil
}

// loadSigningKey decodes the base64 ed25519 private key from the CI
// environment value. It accepts a full 64-byte private key (seed||public)
// or a bare 32-byte seed.
func loadSigningKey(b64 string) (ed25519.PrivateKey, error) {
	if b64 == "" {
		return nil, fmt.Errorf("%s is empty; set it to the base64 ed25519 private key from `sign-manifest -genkey`", signingKeyEnv)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", signingKeyEnv, err)
	}
	switch len(raw) {
	case ed25519.PrivateKeySize: // 64 bytes: seed || public
		return ed25519.PrivateKey(raw), nil
	case ed25519.SeedSize: // 32 bytes: seed only
		return ed25519.NewKeyFromSeed(raw), nil
	default:
		return nil, fmt.Errorf("%s decodes to %d bytes; want %d (private key) or %d (seed)",
			signingKeyEnv, len(raw), ed25519.PrivateKeySize, ed25519.SeedSize)
	}
}

// signFile reads inPath and writes the raw detached ed25519 signature over
// its bytes to outPath.
func signFile(priv ed25519.PrivateKey, inPath, outPath string) error {
	if len(priv) != ed25519.PrivateKeySize {
		return errors.New("private key is not a valid ed25519 private key")
	}
	msg, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", inPath, err)
	}
	sig := ed25519.Sign(priv, msg)
	if err := os.WriteFile(outPath, sig, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}
