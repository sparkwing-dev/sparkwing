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

const signingKeyEnv = "SPARKWING_UPDATE_SIGNING_KEY"

func main() {
	genkey := flag.Bool("genkey", false, "generate an ed25519 keypair and print the public (hex) and private (base64) keys")
	verify := flag.Bool("verify", false, "verify -sig over -in against -pub (hex public key); no private key needed")
	in := flag.String("in", "", "path to the file to sign or verify (e.g. dist/SHA256SUMS)")
	out := flag.String("out", "", "path to write the detached signature to (e.g. dist/SHA256SUMS.sig)")
	sig := flag.String("sig", "", "path to the detached signature to verify (with -verify)")
	pub := flag.String("pub", "", "hex ed25519 public key to verify against (with -verify)")
	flag.Parse()

	if *genkey {
		if err := runGenKey(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sign-manifest:", err)
			os.Exit(1)
		}
		return
	}

	if *verify {
		if *in == "" || *sig == "" || *pub == "" {
			fmt.Fprintln(os.Stderr, "sign-manifest: -verify needs -in, -sig, and -pub")
			os.Exit(2)
		}
		if err := verifyFile(*pub, *in, *sig); err != nil {
			fmt.Fprintf(os.Stderr, "sign-manifest: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stdout, "signature over %s verifies against the given public key\n", *in)
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

func loadSigningKey(b64 string) (ed25519.PrivateKey, error) {
	if b64 == "" {
		return nil, fmt.Errorf("%s is empty; set it to the base64 ed25519 private key from `sign-manifest -genkey`", signingKeyEnv)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", signingKeyEnv, err)
	}
	switch len(raw) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	default:
		return nil, fmt.Errorf("%s decodes to %d bytes; want %d (private key) or %d (seed)",
			signingKeyEnv, len(raw), ed25519.PrivateKeySize, ed25519.SeedSize)
	}
}

func verifyFile(pubHex, inPath, sigPath string) error {
	pub, err := hex.DecodeString(pubHex)
	if err != nil {
		return fmt.Errorf("decode public key hex: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key is %d bytes; want %d", len(pub), ed25519.PublicKeySize)
	}
	allZero := true
	for _, b := range pub {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return errors.New("public key is the all-zero placeholder; the updater build is not armed")
	}
	msg, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", inPath, err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", sigPath, err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
		return fmt.Errorf("signature over %s does not verify against the embedded public key: "+
			"the SPARKWING_UPDATE_SIGNING_KEY secret does not match sparkwingUpdatePubKeyHex", inPath)
	}
	return nil
}

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
