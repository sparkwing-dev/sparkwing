package livechainacceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

type deterministicKMS struct{ keyID string }

func (client deterministicKMS) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	if input.KeyId == nil || *input.KeyId != client.keyID || input.MessageType != types.MessageTypeDigest || input.SigningAlgorithm != types.SigningAlgorithmSpecRsassaPkcs1V15Sha256 {
		return nil, fmt.Errorf("unexpected KMS sign input")
	}
	sum := sha256.Sum256(append([]byte("kms-test-signature\x00"), input.Message...))
	return &kms.SignOutput{KeyId: input.KeyId, Signature: sum[:], SigningAlgorithm: input.SigningAlgorithm}, nil
}

func (client deterministicKMS) Verify(_ context.Context, input *kms.VerifyInput, _ ...func(*kms.Options)) (*kms.VerifyOutput, error) {
	if input.KeyId == nil || *input.KeyId != client.keyID || input.MessageType != types.MessageTypeDigest || input.SigningAlgorithm != types.SigningAlgorithmSpecRsassaPkcs1V15Sha256 {
		return nil, fmt.Errorf("unexpected KMS verify input")
	}
	sum := sha256.Sum256(append([]byte("kms-test-signature\x00"), input.Message...))
	return &kms.VerifyOutput{KeyId: input.KeyId, SignatureValid: bytes.Equal(input.Signature, sum[:]), SigningAlgorithm: input.SigningAlgorithm}, nil
}

func TestKMSSessionAuthorityProducesDeterministicGenesisAcrossProcesses(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "kms-deterministic-genesis", Events: [2]LandEvent{a, b}}
	clock := fixedClock{now: b.LandedAt.Add(time.Minute)}
	firstSigner, firstVerifier, err := NewKMSSessionAuthority(deterministicKMS{keyID: "key/session"}, "key/session", clock)
	if err != nil {
		t.Fatal(err)
	}
	secondSigner, secondVerifier, err := NewKMSSessionAuthority(deterministicKMS{keyID: "key/session"}, "key/session", clock)
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
	if _, exposed := firstVerifier.(SessionSigner); exposed {
		t.Fatal("KMS verifier exposes session signing capability")
	}
}

func TestKMSSessionAuthoritySignsOnlyValidatedSuccessors(t *testing.T) {
	a, b, _ := validTwoFlowScript(t)
	seed := SessionSeed{ID: "kms-successor", Events: [2]LandEvent{a, b}}
	clock := fixedClock{now: b.LandedAt.Add(time.Minute)}
	signer, verifier, err := NewKMSSessionAuthority(deterministicKMS{keyID: "key/session"}, "key/session", clock)
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
	if sealed, err := signer.SealSuccessor(context.Background(), initial, invalid); err == nil || sealed.StateSeal.Signature != "" {
		t.Fatal("invalid successor reached KMS signing")
	}
}
