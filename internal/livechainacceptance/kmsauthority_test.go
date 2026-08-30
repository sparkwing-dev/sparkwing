package livechainacceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

type deterministicKMS struct {
	keyID     string
	signCalls atomic.Int32
	mu        sync.Mutex
	lastSign  []byte
}

func (client *deterministicKMS) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	if input.KeyId == nil || *input.KeyId != client.keyID || input.MessageType != types.MessageTypeDigest || input.SigningAlgorithm != types.SigningAlgorithmSpecRsassaPkcs1V15Sha256 {
		return nil, fmt.Errorf("unexpected KMS sign input")
	}
	client.signCalls.Add(1)
	client.mu.Lock()
	client.lastSign = append([]byte(nil), input.Message...)
	client.mu.Unlock()
	sum := sha256.Sum256(append([]byte("kms-test-signature\x00"), input.Message...))
	return &kms.SignOutput{KeyId: input.KeyId, Signature: sum[:], SigningAlgorithm: input.SigningAlgorithm}, nil
}

func (client *deterministicKMS) Verify(_ context.Context, input *kms.VerifyInput, _ ...func(*kms.Options)) (*kms.VerifyOutput, error) {
	if input.KeyId == nil || *input.KeyId != client.keyID || input.MessageType != types.MessageTypeDigest || input.SigningAlgorithm != types.SigningAlgorithmSpecRsassaPkcs1V15Sha256 {
		return nil, fmt.Errorf("unexpected KMS verify input")
	}
	sum := sha256.Sum256(append([]byte("kms-test-signature\x00"), input.Message...))
	return &kms.VerifyOutput{KeyId: input.KeyId, SignatureValid: bytes.Equal(input.Signature, sum[:]), SigningAlgorithm: input.SigningAlgorithm}, nil
}

type verifyOnlyKMS struct{ client *deterministicKMS }

func (client verifyOnlyKMS) Verify(ctx context.Context, input *kms.VerifyInput, options ...func(*kms.Options)) (*kms.VerifyOutput, error) {
	return client.client.Verify(ctx, input, options...)
}

type scriptedKMSSigner struct {
	output *kms.SignOutput
	err    error
	calls  atomic.Int32
}

func (client *scriptedKMSSigner) Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error) {
	client.calls.Add(1)
	return client.output, client.err
}

type scriptedKMSVerifier struct {
	output *kms.VerifyOutput
	err    error
	calls  atomic.Int32
}

func (client *scriptedKMSVerifier) Verify(context.Context, *kms.VerifyInput, ...func(*kms.Options)) (*kms.VerifyOutput, error) {
	client.calls.Add(1)
	return client.output, client.err
}

const testKMSKeyARN = "arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-1234567890ab"

func TestKMSSessionAuthorityProducesDeterministicGenesisAcrossProcesses(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "kms-deterministic-genesis", Events: [2]LandEvent{a, b}}
	clock := fixedClock{now: b.LandedAt.Add(time.Minute)}
	firstClient := &deterministicKMS{keyID: testKMSKeyARN}
	firstSigner, firstVerifier, err := NewKMSSessionAuthority(firstClient, verifyOnlyKMS{client: firstClient}, firstClient.keyID, clock)
	if err != nil {
		t.Fatal(err)
	}
	secondClient := &deterministicKMS{keyID: firstClient.keyID}
	secondSigner, secondVerifier, err := NewKMSSessionAuthority(secondClient, verifyOnlyKMS{client: secondClient}, secondClient.keyID, clock)
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstSigner.InitialFactory(seed)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondSigner.InitialFactory(seed)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := encodeStoredSession(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := encodeStoredSession(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("independent KMS authorities produced different genesis bytes")
	}
	if err := firstVerifier.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := secondVerifier.Verify(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	wantMessage, err := sessionDigestBytes(first.StateSeal.Digest)
	if err != nil {
		t.Fatal(err)
	}
	firstClient.mu.Lock()
	gotMessage := append([]byte(nil), firstClient.lastSign...)
	firstClient.mu.Unlock()
	if !bytes.Equal(gotMessage, wantMessage) || len(gotMessage) != sha256.Size {
		t.Fatalf("KMS signed message = %x, want canonical digest %x", gotMessage, wantMessage)
	}
	if _, exposed := firstVerifier.(SessionSigner); exposed {
		t.Fatal("KMS verifier exposes session signing capability")
	}
}

func TestKMSSessionAuthoritySignsOnlyValidatedSuccessors(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "kms-successor", Events: [2]LandEvent{a, b}}
	clock := fixedClock{now: b.LandedAt.Add(time.Minute)}
	client := &deterministicKMS{keyID: testKMSKeyARN}
	signer, verifier, err := NewKMSSessionAuthority(client, verifyOnlyKMS{client: client}, client.keyID, clock)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := signer.InitialFactory(seed)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := initial
	next.Version++
	next.PreviousStateDigest = initial.StateSeal.Digest
	next.StateSeal = StateSeal{}
	next.Phase = SessionFailed
	next.TerminalError = "terminal"
	next.PhaseDeadline = time.Time{}
	sealed, err := signer.SealSuccessor(context.Background(), initial, next)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(context.Background(), sealed); err != nil {
		t.Fatal(err)
	}
	invalid := next
	invalid.PreviousStateDigest = genesisSessionStateDigest()
	signCalls := client.signCalls.Load()
	if sealed, err := signer.SealSuccessor(context.Background(), initial, invalid); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("invalid successor reached KMS signing")
	}
	if client.signCalls.Load() != signCalls {
		t.Fatal("invalid successor called KMS Sign before policy validation")
	}
	tamperedCurrent := initial
	tamperedCurrent.StateSeal.Signature += "bad"
	if _, err := signer.SealSuccessor(context.Background(), tamperedCurrent, next); err == nil {
		t.Fatal("invalid current state reached successor signing")
	}
	if client.signCalls.Load() != signCalls {
		t.Fatal("invalid current state called KMS Sign")
	}
}

func TestKMSSessionAuthorityRejectsNonCanonicalKeyARNs(t *testing.T) {
	for _, keyID := range []string{
		"", "key/12345678-1234-1234-1234-1234567890ab", "arn:other:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-1234567890ab",
		"arn:aws:s3:us-east-1:123456789012:key/12345678-1234-1234-1234-1234567890ab", "arn:aws:kms::123456789012:key/12345678-1234-1234-1234-1234567890ab",
		"arn:aws:kms:us-east-1:account:key/12345678-1234-1234-1234-1234567890ab", "arn:aws:kms:us-east-1:123456789012:alias/session",
		"arn:aws:kms:us-east-1:123456789012:key/", "arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-1234567890ab/extra",
		"arn:aws:kms:us-east-1:123456789012:key/arbitrary-name",
	} {
		t.Run(keyID, func(t *testing.T) {
			client := &deterministicKMS{keyID: keyID}
			if _, _, err := NewKMSSessionAuthority(client, verifyOnlyKMS{client: client}, keyID, fixedClock{now: time.Now()}); err == nil {
				t.Fatal("non-canonical KMS key ARN was accepted")
			}
		})
	}
}

func TestKMSSessionAuthorityRejectsInvalidGenesisBeforeSigning(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	for name, mutate := range map[string]func(*SessionSeed){
		"zero_deadline":  func(seed *SessionSeed) { seed.Events[1].Deadline = time.Time{} },
		"early_deadline": func(seed *SessionSeed) { seed.Events[1].Deadline = seed.Events[1].LandedAt.Add(4 * time.Minute) },
		"late_deadline":  func(seed *SessionSeed) { seed.Events[1].Deadline = seed.Events[1].LandedAt.Add(6 * time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			seed := SessionSeed{ID: "invalid-genesis-" + name, Events: [2]LandEvent{a, b}}
			mutate(&seed)
			client := &deterministicKMS{keyID: testKMSKeyARN}
			signer, _, err := NewKMSSessionAuthority(client, verifyOnlyKMS{client: client}, client.keyID, fixedClock{now: b.LandedAt})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := signer.InitialFactory(seed)(context.Background()); err == nil {
				t.Fatal("invalid genesis reached a signature")
			}
			if client.signCalls.Load() != 0 {
				t.Fatal("invalid genesis called KMS Sign")
			}
		})
	}
}

func TestKMSSessionAuthorityRejectsInvalidSignResponses(t *testing.T) {
	keyID := testKMSKeyARN
	valid := &kms.SignOutput{KeyId: &keyID, Signature: []byte("signature"), SigningAlgorithm: types.SigningAlgorithmSpecRsassaPkcs1V15Sha256}
	for name, configure := range map[string]func() (*kms.SignOutput, error){
		"error":           func() (*kms.SignOutput, error) { return nil, errors.New("KMS unavailable") },
		"nil":             func() (*kms.SignOutput, error) { return nil, nil },
		"empty_signature": func() (*kms.SignOutput, error) { copy := *valid; copy.Signature = nil; return &copy, nil },
		"nil_key":         func() (*kms.SignOutput, error) { copy := *valid; copy.KeyId = nil; return &copy, nil },
		"wrong_key": func() (*kms.SignOutput, error) {
			copy, wrong := *valid, "arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			copy.KeyId = &wrong
			return &copy, nil
		},
		"wrong_algorithm": func() (*kms.SignOutput, error) {
			copy := *valid
			copy.SigningAlgorithm = types.SigningAlgorithmSpecRsassaPssSha256
			return &copy, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, b, _ := validTwoFlowScript(t)
			seed := SessionSeed{ID: "sign-response-" + name, Events: [2]LandEvent{a, b}}
			output, signErr := configure()
			signClient := &scriptedKMSSigner{output: output, err: signErr}
			verifyClient := &scriptedKMSVerifier{}
			signer, _, err := NewKMSSessionAuthority(signClient, verifyClient, keyID, fixedClock{now: b.LandedAt})
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := signer.InitialFactory(seed)(context.Background())
			if err == nil || sealed.StateSeal.Signature != "" {
				t.Fatal("invalid KMS Sign response produced a sealed session")
			}
		})
	}
}

func TestKMSSessionAuthorityRejectsInvalidVerifyResponses(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "verify-responses", Events: [2]LandEvent{a, b}}
	validClient := &deterministicKMS{keyID: testKMSKeyARN}
	signer, _, err := NewKMSSessionAuthority(validClient, verifyOnlyKMS{client: validClient}, validClient.keyID, fixedClock{now: b.LandedAt})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := signer.InitialFactory(seed)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keyID := testKMSKeyARN
	valid := &kms.VerifyOutput{KeyId: &keyID, SignatureValid: true, SigningAlgorithm: types.SigningAlgorithmSpecRsassaPkcs1V15Sha256}
	for name, configure := range map[string]func() (*kms.VerifyOutput, error){
		"error":   func() (*kms.VerifyOutput, error) { return nil, errors.New("KMS unavailable") },
		"nil":     func() (*kms.VerifyOutput, error) { return nil, nil },
		"invalid": func() (*kms.VerifyOutput, error) { copy := *valid; copy.SignatureValid = false; return &copy, nil },
		"nil_key": func() (*kms.VerifyOutput, error) { copy := *valid; copy.KeyId = nil; return &copy, nil },
		"wrong_key": func() (*kms.VerifyOutput, error) {
			copy, wrong := *valid, "arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			copy.KeyId = &wrong
			return &copy, nil
		},
		"wrong_algorithm": func() (*kms.VerifyOutput, error) {
			copy := *valid
			copy.SigningAlgorithm = types.SigningAlgorithmSpecRsassaPssSha256
			return &copy, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			output, verifyErr := configure()
			verifyClient := &scriptedKMSVerifier{output: output, err: verifyErr}
			_, verifier, err := NewKMSSessionAuthority(&scriptedKMSSigner{}, verifyClient, keyID, fixedClock{now: b.LandedAt})
			if err != nil {
				t.Fatal(err)
			}
			if err := verifier.Verify(context.Background(), sealed); err == nil {
				t.Fatal("invalid KMS Verify response authenticated a session")
			}
		})
	}
}

func TestKMSSessionVerifierRejectsPolicyBeforeKMS(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "verify-policy", Events: [2]LandEvent{a, b}}
	validClient := &deterministicKMS{keyID: testKMSKeyARN}
	signer, _, err := NewKMSSessionAuthority(validClient, verifyOnlyKMS{client: validClient}, validClient.keyID, fixedClock{now: b.LandedAt})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := signer.InitialFactory(seed)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*StateSeal){
		"domain": func(seal *StateSeal) { seal.Domain += "bad" },
		"schema": func(seal *StateSeal) { seal.SchemaVersion += "bad" },
		"key": func(seal *StateSeal) {
			seal.KeyID = "arn:aws:kms:us-east-1:123456789012:key/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		},
		"algorithm": func(seal *StateSeal) { seal.Algorithm += "bad" },
		"signed_at": func(seal *StateSeal) { seal.SignedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			mutant := sealed
			mutate(&mutant.StateSeal)
			mutant.StateSeal.Signature = ""
			mutant, err = sessionWithStateDigest(mutant)
			if err != nil {
				t.Fatal(err)
			}
			mutant.StateSeal.Signature = base64.RawStdEncoding.EncodeToString([]byte("valid"))
			verifyClient := &scriptedKMSVerifier{output: &kms.VerifyOutput{KeyId: &mutant.StateSeal.KeyID, SignatureValid: true, SigningAlgorithm: types.SigningAlgorithmSpecRsassaPkcs1V15Sha256}}
			verifier := kmsSessionVerifier{client: verifyClient, keyID: testKMSKeyARN}
			if err := verifier.Verify(context.Background(), mutant); err == nil {
				t.Fatal("mutated seal policy was accepted")
			}
			if verifyClient.calls.Load() != 0 {
				t.Fatal("mutated seal policy reached KMS Verify")
			}
		})
	}
	malformed := sealed
	malformed.StateSeal.Signature = "%%%"
	verifyClient := &scriptedKMSVerifier{}
	if err := (kmsSessionVerifier{client: verifyClient, keyID: testKMSKeyARN}).Verify(context.Background(), malformed); err == nil || verifyClient.calls.Load() != 0 {
		t.Fatal("malformed signature reached KMS Verify")
	}
}
