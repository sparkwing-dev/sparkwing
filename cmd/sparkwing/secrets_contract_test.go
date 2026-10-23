package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type secretManagerFake struct {
	list   func() (controllerwire.Secrets, error)
	read   func(controllerwire.SecretSelector) (controllerwire.SecretValue, error)
	write  func(controllerwire.SecretWrite) (controllerwire.SecretWritten, error)
	del    func(controllerwire.SecretSelector) (controllerwire.SecretDeletion, error)
	rotate func(controllerwire.SecretRotationRequest) (controllerwire.SecretRotation, error)
}

func (f secretManagerFake) ListSecrets(context.Context) (controllerwire.Secrets, error) {
	return f.list()
}

func (f secretManagerFake) ReadSecret(_ context.Context, s controllerwire.SecretSelector) (controllerwire.SecretValue, error) {
	return f.read(s)
}

func (f secretManagerFake) WriteSecret(_ context.Context, s controllerwire.SecretWrite) (controllerwire.SecretWritten, error) {
	return f.write(s)
}

func (f secretManagerFake) DeleteSecret(_ context.Context, s controllerwire.SecretSelector) (controllerwire.SecretDeletion, error) {
	return f.del(s)
}

func (f secretManagerFake) RotateSecrets(_ context.Context, s controllerwire.SecretRotationRequest) (controllerwire.SecretRotation, error) {
	return f.rotate(s)
}

func secretInvocation(t *testing.T, args ...string) clisurface.Invocation {
	t.Helper()
	inv, err := clisurface.Parse("sparkwing", append([]string{"secrets"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestSecretManagementPreservesValueAndRestrictions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte("file\nvalue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args        []string
		stdin, want string
	}{
		{[]string{"--value", "explicit\nvalue"}, "ignored", "explicit\nvalue"},
		{[]string{"--value", ""}, "ignored", ""},
		{[]string{"--file", file}, "ignored", "file\nvalue\n"},
		{nil, "stdin\nvalue\n", "stdin\nvalue\n"},
	} {
		var got controllerwire.SecretWrite
		manager := secretManagerFake{write: func(input controllerwire.SecretWrite) (controllerwire.SecretWritten, error) {
			got = input
			return controllerwire.SecretWritten{Secret: controllerwire.SecretMetadata{Name: input.Name, Pipeline: input.Pipeline, Masked: input.Masked, OnlyOn: input.OnlyOn, Bound: true}}, nil
		}}
		args := append([]string{"set", "--secret", "DEPLOY_KEY", "--pipeline", "deploy", "--only-on", "prod", "-o", "json"}, tc.args...)
		var out bytes.Buffer
		err := manageSecrets(t.Context(), secretInvocation(t, args...), manager, strings.NewReader(tc.stdin), &out, io.Discard, false)
		if err != nil || got.Value != tc.want || got.Name != "DEPLOY_KEY" || got.Pipeline != "deploy" || !got.Masked || got.Shared || got.OnlyOn != "prod" || got.IdempotencyKey == "" {
			t.Fatalf("write: %+v %v", got, err)
		}
		var result controllerwire.SecretWritten
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Secret.OnlyOn != "prod" || strings.Contains(out.String(), `"value"`) {
			t.Fatalf("output leaked value or lost restriction: %s %v", &out, err)
		}
	}
}

func TestSecretManagementLocalDefaultsAndVariables(t *testing.T) {
	for _, args := range [][]string{{}, {"--pipeline", "deploy"}, {"--shared"}} {
		manager := secretManagerFake{write: func(input controllerwire.SecretWrite) (controllerwire.SecretWritten, error) {
			if input.Masked || input.Shared != (input.Pipeline == "") {
				t.Fatalf("local variable scope: %+v", input)
			}
			return controllerwire.SecretWritten{Secret: controllerwire.SecretMetadata{Name: input.Name}}, nil
		}}
		inv := secretInvocation(t, append([]string{"set", "--secret", "REGION", "--value", "us-east-1", "--variable", "-o", "plain"}, args...)...)
		var out bytes.Buffer
		if err := manageSecrets(t.Context(), inv, manager, strings.NewReader(""), &out, io.Discard, true); err != nil || out.String() != "REGION\n" {
			t.Fatalf("write: %s %v", &out, err)
		}
	}
}

func TestSecretManagementListsOnlyMetadata(t *testing.T) {
	manager := secretManagerFake{list: func() (controllerwire.Secrets, error) {
		return controllerwire.Secrets{Secrets: []controllerwire.SecretMetadata{{Name: "TOKEN", Pipeline: "deploy", Masked: true, OnlyOn: "prod", Principal: "alice", Bound: true, CreatedAt: 1, UpdatedAt: 2}, {Name: "REGION", Shared: true}}}, nil
	}}
	for _, format := range []string{"pretty", "json", "plain"} {
		var out bytes.Buffer
		inv := secretInvocation(t, "list", "--search", "token", "-o", format)
		if err := manageSecrets(t.Context(), inv, manager, nil, &out, io.Discard, false); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "TOKEN") || strings.Contains(out.String(), "REGION") {
			t.Fatalf("filter: %s", &out)
		}
		if format == "plain" && out.String() != "TOKEN\n" {
			t.Fatalf("plain: %q", &out)
		}
		if format == "json" {
			var result controllerwire.SecretMetadata
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.OnlyOn != "prod" || result.Principal != "alice" || !result.Bound || !result.Masked || result.UpdatedAt != 2 {
				t.Fatalf("metadata: %+v %v", result, err)
			}
		}
		if format == "pretty" && (!strings.Contains(out.String(), "ONLY_ON") || !strings.Contains(out.String(), "prod")) {
			t.Fatalf("restriction invisible: %s", &out)
		}
	}
	manager.list = func() (controllerwire.Secrets, error) { return controllerwire.Secrets{}, nil }
	for _, format := range []string{"json", "plain"} {
		var out bytes.Buffer
		if err := manageSecrets(t.Context(), secretInvocation(t, "list", "-o", format), manager, nil, &out, io.Discard, false); err != nil || out.Len() != 0 {
			t.Fatalf("empty %s: %q %v", format, &out, err)
		}
	}
}

func TestSecretManagementExplicitReadAndExactDeletion(t *testing.T) {
	selector := controllerwire.SecretSelector{Name: "TOKEN", Pipeline: "deploy"}
	manager := secretManagerFake{
		read: func(got controllerwire.SecretSelector) (controllerwire.SecretValue, error) {
			if got != selector {
				t.Fatalf("read selector: %+v", got)
			}
			return controllerwire.SecretValue{SecretMetadata: controllerwire.SecretMetadata{Name: "TOKEN", Pipeline: "deploy", Masked: true}, Value: "secret\nvalue"}, nil
		},
		del: func(got controllerwire.SecretSelector) (controllerwire.SecretDeletion, error) {
			if got != selector {
				t.Fatalf("delete selector: %+v", got)
			}
			return controllerwire.SecretDeletion{Name: "TOKEN", Pipeline: "deploy", Deleted: false}, nil
		},
	}
	for _, format := range []string{"pretty", "json", "plain"} {
		var out bytes.Buffer
		if err := manageSecrets(t.Context(), secretInvocation(t, "get", "--secret", "TOKEN", "--pipeline", "deploy", "-o", format), manager, nil, &out, io.Discard, false); err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var result controllerwire.SecretValue
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Value != "secret\nvalue" {
				t.Fatalf("read: %+v %v", result, err)
			}
		} else if out.String() != "secret\nvalue" {
			t.Fatalf("value changed: %q", &out)
		}
		out.Reset()
		if err := manageSecrets(t.Context(), secretInvocation(t, "delete", "--secret", "TOKEN", "--pipeline", "deploy", "-o", format), manager, nil, &out, io.Discard, false); err != nil {
			t.Fatal(err)
		}
		if format == "json" && !strings.Contains(out.String(), `"deleted":false`) {
			t.Fatalf("absent deletion: %s", &out)
		}
	}
}

func TestSecretManagementRotationPreservesSkippedRows(t *testing.T) {
	want := controllerwire.SecretRotation{Rotated: 3, Skipped: []controllerwire.SecretSelector{{Name: "TOKEN", Pipeline: "deploy"}}, Replayed: true}
	manager := secretManagerFake{rotate: func(input controllerwire.SecretRotationRequest) (controllerwire.SecretRotation, error) {
		if input.IdempotencyKey == "" {
			t.Fatal("missing key")
		}
		return want, nil
	}}
	for _, format := range []string{"pretty", "json", "plain"} {
		var out bytes.Buffer
		if err := manageSecrets(t.Context(), secretInvocation(t, "rotate", "-o", format), manager, nil, &out, io.Discard, false); err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var got controllerwire.SecretRotation
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("rotation: %+v %v", got, err)
			}
		} else if !strings.Contains(out.String(), "TOKEN") || !strings.Contains(out.String(), "deploy") || strings.Contains(out.String(), "can now be dropped") {
			t.Fatalf("skipped row lost: %s", &out)
		}
	}
}

func TestSecretManagementFailureEmitsNoSuccess(t *testing.T) {
	failure := &apierr.Error{Code: apierr.Forbidden, Message: "team admin required"}
	manager := secretManagerFake{write: func(controllerwire.SecretWrite) (controllerwire.SecretWritten, error) {
		return controllerwire.SecretWritten{}, failure
	}}
	var out bytes.Buffer
	err := manageSecrets(t.Context(), secretInvocation(t, "set", "--secret", "TOKEN", "--value", "secret", "-o", "json"), manager, nil, &out, io.Discard, false)
	if !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("failure: %s %v", &out, err)
	}
}

type failingSecretInput struct{}

func (failingSecretInput) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSecretManagementInputFailureDoesNotWrite(t *testing.T) {
	manager := secretManagerFake{write: func(controllerwire.SecretWrite) (controllerwire.SecretWritten, error) {
		t.Fatal("failed input was sent")
		return controllerwire.SecretWritten{}, nil
	}}
	if err := manageSecrets(t.Context(), secretInvocation(t, "set", "--secret", "TOKEN"), manager, failingSecretInput{}, io.Discard, io.Discard, false); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("input failure: %v", err)
	}
}

func TestSecretManagementRejectsInvalidInputBeforeConnection(t *testing.T) {
	for _, args := range [][]string{
		{"set", "--secret", "../TOKEN", "--value", "secret"},
		{"set", "--secret", "TOKEN", "--value", "secret", "--file", "missing"},
		{"set", "--secret", "TOKEN", "--pipeline", "deploy", "--shared"},
		{"list", "-o", "table"},
		{"get"},
		{"delete", "TOKEN"},
	} {
		if err := runSecretArgs(args); err == nil {
			t.Fatalf("accepted: %v", args)
		}
	}
}

func TestSecretManagementCommandUsesFinalControllerAPI(t *testing.T) {
	path := profilesFixturePath(t)
	if err := writeConnectionFixture(path, "prod", "https://controller.example", "private-credential"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	profileHTTPFixture(t, func(req *http.Request) (int, string) {
		calls++
		if req.Method != "POST" || req.URL.Path != "/api/v1/secrets" || req.Header.Get("Authorization") != "Bearer private-credential" || req.Header.Get("Idempotency-Key") == "" {
			t.Fatalf("request: %s %s", req.Method, req.URL)
		}
		var input controllerwire.SecretWrite
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.OnlyOn != "prod" || input.Value != "private-value" || !input.Masked || input.Shared {
			t.Fatalf("write: %+v", input)
		}
		return 200, `{"secret":{"name":"TOKEN","principal":"alice","masked":true,"shared":false,"bound":true,"only_on":"prod","created_at":1,"updated_at":1},"replayed":false}`
	})
	var result error
	out := captureStdout(t, func() {
		result = runSecretArgs([]string{"set", "--secret", "TOKEN", "--value", "private-value", "--only-on", "prod", "--profile", "prod", "-o", "json"})
	})
	if result != nil || calls != 1 || strings.Contains(out, "private-value") || strings.Contains(out, "private-credential") || !strings.Contains(out, `"only_on":"prod"`) {
		t.Fatalf("command: %s %v", out, result)
	}
}

func TestSecretSetWarnsForShortValues(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		for _, local := range []bool{false, true} {
			manager := secretManagerFake{write: func(input controllerwire.SecretWrite) (controllerwire.SecretWritten, error) {
				if input.Value != "abc" || !input.Masked {
					t.Fatal("secret changed")
				}
				return controllerwire.SecretWritten{Secret: controllerwire.SecretMetadata{Name: input.Name, Masked: true}}, nil
			}}
			var out, diagnostics bytes.Buffer
			inv := secretInvocation(t, "set", "--secret", "TOKEN", "--value", "abc", "-o", format)
			if err := manageSecrets(t.Context(), inv, manager, nil, &out, &diagnostics, local); err != nil {
				t.Fatal(err)
			}
			if diagnostics.String() != "warning: "+secrets.ShortValueWarning+"\n" {
				t.Fatalf("missing warning: %s", &diagnostics)
			}
			if format == "json" {
				var result controllerwire.SecretWritten
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Warning != "" || strings.Contains(out.String(), `"warning"`) {
					t.Fatalf("JSON receipt repeats warning: %s %v", &out, err)
				}
			}
		}
	}
}

func TestLocalSecretIntakeDoesNotWarnOrChangeValue(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "warning")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	original := os.Stderr
	os.Stderr = file
	defer func() { os.Stderr = original }()
	values, err := resolveBurstSecrets(t.Context(), []string{"TOKEN"}, []nodeproto.SecretDeclaration{{Name: "TOKEN"}}, map[string]string{"TOKEN": "abc"})
	if err != nil || values["TOKEN"] != "abc" {
		t.Fatalf("local secret refused: %v %v", values, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	warning, err := io.ReadAll(file)
	if err != nil || len(warning) != 0 {
		t.Fatalf("unexpected warning: %s %v", warning, err)
	}
}

func TestSecretSetWarnsOnlyAfterSavingAShortSecret(t *testing.T) {
	failure := errors.New("save failed")
	for _, test := range []struct {
		name  string
		flags []string
		err   error
	}{
		{name: "failed", flags: []string{"--value", "abc"}, err: failure},
		{name: "long", flags: []string{"--value", "abcd"}},
		{name: "variable", flags: []string{"--value", "abc", "--variable"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := secretManagerFake{write: func(input controllerwire.SecretWrite) (controllerwire.SecretWritten, error) {
				return controllerwire.SecretWritten{Secret: controllerwire.SecretMetadata{Name: input.Name}}, test.err
			}}
			flags := append([]string{"set", "--secret", "TOKEN", "-o", "plain"}, test.flags...)
			var out, diagnostics bytes.Buffer
			err := manageSecrets(t.Context(), secretInvocation(t, flags...), manager, nil, &out, &diagnostics, true)
			if !errors.Is(err, test.err) || diagnostics.Len() != 0 {
				t.Fatalf("save error=%v diagnostics=%q", err, &diagnostics)
			}
		})
	}
}
