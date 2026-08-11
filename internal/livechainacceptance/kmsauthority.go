package livechainacceptance

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

const (
	kmsSessionSealDomain    = "sparkwing/livechainacceptance/session/v1"
	kmsSessionSealSchema    = "v1"
	kmsSessionSealAlgorithm = "AWS_KMS_RSASSA_PKCS1_V1_5_SHA_256"
)

type KMSSignerAPI interface {
	Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error)
}

type KMSVerifierAPI interface {
	Verify(context.Context, *kms.VerifyInput, ...func(*kms.Options)) (*kms.VerifyOutput, error)
}

type kmsSessionSigner struct {
	client   KMSSignerAPI
	keyID    string
	clock    Clock
	verifier kmsSessionVerifier
}

type kmsSessionVerifier struct {
	client KMSVerifierAPI
	keyID  string
}

func NewKMSSessionAuthority(signClient KMSSignerAPI, verifyClient KMSVerifierAPI, keyID string, clock Clock) (SessionSigner, SessionVerifier, error) {
	if signClient == nil || verifyClient == nil || clock == nil {
		return nil, nil, fmt.Errorf("KMS session authority requires isolated signing and verification clients plus a clock")
	}
	if _, exposesSigning := verifyClient.(KMSSignerAPI); exposesSigning {
		return nil, nil, fmt.Errorf("KMS verification client exposes signing capability")
	}
	if err := validateKMSKeyARN(keyID); err != nil {
		return nil, nil, err
	}
	verifier := kmsSessionVerifier{client: verifyClient, keyID: keyID}
	return &kmsSessionSigner{client: signClient, keyID: keyID, clock: clock, verifier: verifier}, verifier, nil
}

func (signer *kmsSessionSigner) InitialFactory(seed SessionSeed) InitialSessionFactory {
	return func(ctx context.Context) (Session, error) {
		if err := validateConsecutiveEvents(seed.Events[0], seed.Events[1]); err != nil {
			return Session{}, fmt.Errorf("validate deterministic genesis events: %w", err)
		}
		initial := initialSession(seed)
		initial.PhaseDeadline = seed.Events[1].Deadline
		if err := validateInitialSealRequest(seed, initial); err != nil {
			return Session{}, err
		}
		if !initial.PhaseDeadline.After(seed.Events[1].LandedAt) {
			return Session{}, fmt.Errorf("deterministic genesis deadline does not follow the second land")
		}
		return signer.sign(ctx, initial, seed.Events[1].LandedAt)
	}
}

func (signer *kmsSessionSigner) SealSuccessor(ctx context.Context, current, next Session) (Session, error) {
	if err := signer.verifier.Verify(ctx, current); err != nil {
		return Session{}, fmt.Errorf("verify current KMS session state: %w", err)
	}
	now := signer.clock.Now()
	if err := validateSuccessorSealRequest(current, next, now); err != nil {
		return Session{}, err
	}
	return signer.sign(ctx, next, now)
}

func (signer *kmsSessionSigner) sign(ctx context.Context, session Session, signedAt time.Time) (Session, error) {
	session.StateSeal = StateSeal{
		Domain:        kmsSessionSealDomain,
		SchemaVersion: kmsSessionSealSchema,
		KeyID:         signer.keyID,
		Algorithm:     kmsSessionSealAlgorithm,
		SignedAt:      signedAt.UTC(),
	}
	withDigest, err := sessionWithStateDigest(session)
	if err != nil {
		return Session{}, err
	}
	digest, err := sessionDigestBytes(withDigest.StateSeal.Digest)
	if err != nil {
		return Session{}, err
	}
	output, err := signer.client.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(signer.keyID),
		Message:          digest,
		MessageType:      types.MessageTypeDigest,
		SigningAlgorithm: types.SigningAlgorithmSpecRsassaPkcs1V15Sha256,
	})
	if err != nil {
		return Session{}, fmt.Errorf("KMS sign acceptance session: %w", err)
	}
	if output == nil || len(output.Signature) == 0 || output.KeyId == nil || *output.KeyId != signer.keyID || output.SigningAlgorithm != types.SigningAlgorithmSpecRsassaPkcs1V15Sha256 {
		return Session{}, fmt.Errorf("KMS returned an incomplete or mismatched session signature")
	}
	withDigest.StateSeal.Signature = base64.RawStdEncoding.EncodeToString(output.Signature)
	return withDigest, nil
}

func (verifier kmsSessionVerifier) Verify(ctx context.Context, session Session) error {
	seal := session.StateSeal
	if seal.Domain != kmsSessionSealDomain || seal.SchemaVersion != kmsSessionSealSchema || seal.KeyID != verifier.keyID || seal.Algorithm != kmsSessionSealAlgorithm || seal.SignedAt.IsZero() {
		return fmt.Errorf("acceptance session KMS seal policy mismatch")
	}
	digest, err := digestSessionState(session)
	if err != nil {
		return err
	}
	if digest != seal.Digest {
		return fmt.Errorf("acceptance session KMS digest mismatch")
	}
	message, err := sessionDigestBytes(digest)
	if err != nil {
		return err
	}
	signature, err := base64.RawStdEncoding.DecodeString(seal.Signature)
	if err != nil || len(signature) == 0 {
		return fmt.Errorf("acceptance session KMS signature encoding is invalid")
	}
	output, err := verifier.client.Verify(ctx, &kms.VerifyInput{
		KeyId:            aws.String(verifier.keyID),
		Message:          message,
		MessageType:      types.MessageTypeDigest,
		Signature:        signature,
		SigningAlgorithm: types.SigningAlgorithmSpecRsassaPkcs1V15Sha256,
	})
	if err != nil {
		return fmt.Errorf("KMS verify acceptance session: %w", err)
	}
	if output == nil || !output.SignatureValid || output.KeyId == nil || *output.KeyId != verifier.keyID || output.SigningAlgorithm != types.SigningAlgorithmSpecRsassaPkcs1V15Sha256 {
		return fmt.Errorf("acceptance session KMS signature is invalid")
	}
	return nil
}

func sessionDigestBytes(value string) ([]byte, error) {
	encoded := strings.TrimPrefix(value, "sha256:")
	if len(encoded) != sha256.Size*2 {
		return nil, fmt.Errorf("acceptance session digest has invalid length")
	}
	digest, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode acceptance session digest: %w", err)
	}
	return digest, nil
}

var kmsAccountPattern = regexp.MustCompile(`^[0-9]{12}$`)
var kmsKeyIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|mrk-[0-9a-f]{32})$`)

func validateKMSKeyARN(value string) error {
	parsed, err := arn.Parse(value)
	if err != nil {
		return fmt.Errorf("parse canonical KMS key ARN: %w", err)
	}
	switch parsed.Partition {
	case "aws", "aws-us-gov", "aws-cn":
	default:
		return fmt.Errorf("canonical KMS key ARN has unsupported partition %q", parsed.Partition)
	}
	keyID, isKey := strings.CutPrefix(parsed.Resource, "key/")
	if parsed.Service != "kms" || parsed.Region == "" || !kmsAccountPattern.MatchString(parsed.AccountID) || !isKey || !kmsKeyIDPattern.MatchString(keyID) {
		return fmt.Errorf("KMS session authority requires a canonical key resource ARN")
	}
	return nil
}
