package mailer_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/sparkwing-dev/sparkwing/internal/mailer"
)

type capturedRequest struct {
	method, path, auth string
	body               map[string]any
}

func newSESServer(t *testing.T, status int) (*httptest.Server, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got.body); err != nil {
			t.Errorf("request body is not JSON: %v: %s", err, raw)
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.Header().Set("X-Amzn-ErrorType", "BadRequestException")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"bad request"}`))
			return
		}
		_, _ = w.Write([]byte(`{"MessageId":"m-1"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func newTestSES(t *testing.T, srvURL string, cfg mailer.SESConfig) *mailer.SES {
	t.Helper()
	// Keep the developer's AWS profile and files out of the default chain.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", empty)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", empty)
	t.Setenv("AWS_PROFILE", "")
	m, err := mailer.NewSES(context.Background(), cfg, func(o *sesv2.Options) {
		o.Region = "us-west-2"
		o.BaseEndpoint = aws.String(srvURL)
		o.Credentials = credentials.NewStaticCredentialsProvider("AKIDTEST", "secret", "")
		o.RetryMaxAttempts = 1
	})
	if err != nil {
		t.Fatalf("NewSES: %v", err)
	}
	return m
}

var testMessage = mailer.Message{
	To:      "invitee@example.com",
	Subject: "Ada invited you to Ops on Sparkwing",
	Text:    "plain body",
	HTML:    "<p>html body</p>",
}

func TestSESSendSignsSendEmailRequest(t *testing.T) {
	srv, got := newSESServer(t, http.StatusOK)
	m := newTestSES(t, srv.URL, mailer.SESConfig{From: "noreply@sparkwing.example", ConfigurationSet: "transactional"})

	if err := m.Send(context.Background(), testMessage); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.method != http.MethodPost || got.path != "/v2/email/outbound-emails" {
		t.Fatalf("request = %s %s, want POST /v2/email/outbound-emails", got.method, got.path)
	}
	if !strings.HasPrefix(got.auth, "AWS4-HMAC-SHA256") {
		t.Fatalf("Authorization = %q, want SigV4", got.auth)
	}
	if got.body["FromEmailAddress"] != "noreply@sparkwing.example" {
		t.Errorf("FromEmailAddress = %v", got.body["FromEmailAddress"])
	}
	if got.body["ConfigurationSetName"] != "transactional" {
		t.Errorf("ConfigurationSetName = %v", got.body["ConfigurationSetName"])
	}
	raw, _ := json.Marshal(got.body)
	var req struct {
		Destination struct{ ToAddresses []string }
		Content     struct {
			Simple struct {
				Subject struct{ Data, Charset string }
				Body    struct {
					Text struct{ Data, Charset string }
					HTML struct {
						Data, Charset string
					} `json:"Html"`
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Destination.ToAddresses) != 1 || req.Destination.ToAddresses[0] != testMessage.To {
		t.Errorf("ToAddresses = %v", req.Destination.ToAddresses)
	}
	s := req.Content.Simple
	for name, c := range map[string]struct{ Data, Charset, Want string }{
		"subject": {s.Subject.Data, s.Subject.Charset, testMessage.Subject},
		"text":    {s.Body.Text.Data, s.Body.Text.Charset, testMessage.Text},
		"html":    {s.Body.HTML.Data, s.Body.HTML.Charset, testMessage.HTML},
	} {
		if c.Data != c.Want || c.Charset != "UTF-8" {
			t.Errorf("%s = %q (%q), want %q (UTF-8)", name, c.Data, c.Charset, c.Want)
		}
	}
}

func TestSESSendOmitsEmptyConfigurationSet(t *testing.T) {
	srv, got := newSESServer(t, http.StatusOK)
	m := newTestSES(t, srv.URL, mailer.SESConfig{From: "noreply@sparkwing.example"})

	if err := m.Send(context.Background(), testMessage); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, ok := got.body["ConfigurationSetName"]; ok {
		t.Fatalf("ConfigurationSetName present: %v", got.body["ConfigurationSetName"])
	}
}

func TestSESSendSurfacesErrorStatus(t *testing.T) {
	srv, _ := newSESServer(t, http.StatusBadRequest)
	m := newTestSES(t, srv.URL, mailer.SESConfig{From: "noreply@sparkwing.example"})

	err := m.Send(context.Background(), testMessage)
	if err == nil {
		t.Fatal("Send succeeded against a 400 response")
	}
	if strings.Contains(err.Error(), testMessage.Text) || strings.Contains(err.Error(), testMessage.HTML) {
		t.Fatalf("error leaks the body: %v", err)
	}
}

func TestNewSESRequiresSender(t *testing.T) {
	if _, err := mailer.NewSES(context.Background(), mailer.SESConfig{}); err == nil {
		t.Fatal("NewSES accepted an empty From")
	}
}
