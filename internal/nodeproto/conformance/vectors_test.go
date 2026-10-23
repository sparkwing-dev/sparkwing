package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type vectorSecrets []string

func (s vectorSecrets) ContainsSecret(id string) bool {
	for _, v := range s {
		if v != "" && strings.Contains(id, v) {
			return true
		}
	}
	return false
}

type declarationVector struct {
	Name, Raw, Canonical, SHA256 string
	Code                         apierr.Code
	Secrets                      []string
	SourceJob                    string              `json:"source_job"`
	ExistingJobs                 []nodeproto.JobSpec `json:"existing_jobs"`
}

type v2Vectors struct {
	Plans       []declarationVector
	InnerErrors []struct {
		Name, Raw string
		Valid     bool
	} `json:"inner_errors"`
}

func readVectors(t *testing.T) v2Vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/v2-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors v2Vectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func TestSharedV2DeclarationAndErrorVectors(t *testing.T) {
	corpus := readVectors(t)
	for _, v := range corpus.Plans {
		t.Run(v.Name, func(t *testing.T) {
			hash, err := runspec.CanonicalHash(json.RawMessage(v.Raw))
			if err != nil || string(hash) != v.SHA256 {
				t.Fatalf("hash = %s, %v, want %s", hash, err, v.SHA256)
			}
			canonicalHash, err := runspec.CanonicalHash(json.RawMessage(v.Canonical))
			if err != nil || canonicalHash != hash {
				t.Fatal("canonical vector and input differ")
			}
			_, err = nodeproto.ValidatePlan(json.RawMessage(v.Raw), nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: vectorSecrets(v.Secrets), Expansion: v.SourceJob != "", SourceJob: v.SourceJob, ExistingJobs: v.ExistingJobs})
			if v.Code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var failure *apierr.Error
				if !errors.As(err, &failure) || failure.Code != v.Code {
					t.Fatalf("validation = %v, want %s", err, v.Code)
				}
			}
		})
	}
	for _, v := range corpus.InnerErrors {
		t.Run("error/"+v.Name, func(t *testing.T) {
			_, err := apierr.DecodeInner([]byte(v.Raw))
			if (err == nil) != v.Valid {
				t.Fatalf("error validity = %v, want %t", err, v.Valid)
			}
		})
	}
}

func TestGoSDKBuildsSharedV2Declarations(t *testing.T) {
	builders := map[string]func(*sparkwing.Plan){
		"source-omitted": func(p *sparkwing.Plan) {
			p.Checkout(sparkwing.Checkout{})
		},
		"source-explicit-false": func(p *sparkwing.Plan) {
			value := false
			p.Checkout(sparkwing.Checkout{Tags: &value, FullHistory: &value})
		},
		"approval-condition": func(p *sparkwing.Plan) {
			p.Approval("approve", sparkwing.ApprovalConfig{}).SkipIf(func(context.Context) bool { return false })
		},
		"snake-case-job": func(p *sparkwing.Plan) {
			sparkwing.Job(p, "compile", vectorWork{}).ContinueOnError()
		},
	}
	for _, vector := range readVectors(t).Plans {
		build, ok := builders[vector.Name]
		if !ok {
			continue
		}
		delete(builders, vector.Name)
		t.Run(vector.Name, func(t *testing.T) {
			plan := sparkwing.NewPlan()
			build(plan)
			raw, err := json.Marshal(sdkserve.DefaultOperations.Document(plan))
			if err != nil {
				t.Fatal(err)
			}
			hash, err := runspec.CanonicalHash(json.RawMessage(raw))
			if err != nil || string(hash) != vector.SHA256 {
				t.Fatalf("SDK declaration = %s, hash = %s, %v; want %s", raw, hash, err, vector.Canonical)
			}
		})
	}
	if len(builders) != 0 {
		t.Fatalf("SDK builders have no shared vectors: %v", builders)
	}
}

type vectorWork struct{}

func (vectorWork) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	work.FailurePolicy(sparkwing.CollectAll)
	sparkwing.Step(work, "run", func(context.Context) error { return nil })
	return nil, nil
}
