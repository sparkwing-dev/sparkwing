package envredact

import "testing"

func TestCredentialName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"GITHUB_TOKEN", true},
		{"SPARKWING_AGENT_TOKEN", true},
		{"SPARKWING_CACHE_TOKEN", true},
		{"SPARKWING_LEASE_TOKEN", true},
		{"SPARKWING_SECRETS_KEY", true},
		{"SPARKWING_PG_URL", true},
		{"DATABASE_URL", true},
		{"PGPASSWORD", true},
		{"PGURL", true},
		{"AWS_SECRET_ACCESS_KEY", true},
		{"DB_PASSWORD", true},
		{"POSTGRES_DSN", true},
		{"SERVICE_PRIVATE_KEY", true},
		{"CLIENT_CERT", true},
		{"NODE_EXTRA_CA_PEM", true},
		{"BASIC_AUTH", true},
		{"GOOGLE_APPLICATION_CREDENTIALS", true},
		{"npm_token", true},
		{"GITHUB_PAT", true},
		{"PAT", true},
		{"CI_JWT", true},
		{"SESSION_COOKIE", true},
		{"WEBHOOK_SIGNATURE", true},
		{"GIT_AUTHOR_NAME", false},
		{"GIT_AUTHOR_EMAIL", false},
		{"GOPRIVATE", false},
		{"SSH_KEY_DIR", false},
		{"DOCKER_TLS_CERTDIR", false},
		{"SPARKWING_REQUIRE_AUTH", false},
		{"SPARKWING_CACHE_ALLOW_UNAUTHENTICATED", false},
		{"SPARKWING_RUN_ID", false},
		{"GITHUB_REPOSITORY", false},
		{"PATH", false},
		{"GOPATH", false},
		{"COMPAT_MODE", false},
		{"SSH_AUTH_SOCK", false},
		{"DOCKER_CERT_PATH", false},
		{"HOME", false},
		{"_SPARKWING_RETRY_REPO_DIR", false},
		{"_SPARKWING_RETRY_REPO_URL", false},
		{"_SPARKWING_RETRY_REVISION", false},
		{"_SPARKWING_RETRY_PLAN_HASH", false},
	}
	for _, c := range cases {
		if got := CredentialName(c.name); got != c.want {
			t.Errorf("CredentialName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCredentialValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"bearer", "Bearer abc.def.ghi", true},
		{"bearer lowercase", "bearer abc.def.ghi", true},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----", true},
		{"json token", `{"kind":"sa","token":"abc"}`, true},
		{"json nested password", `{"db":{"password":"hunter2"}}`, true},
		{"json plain", `{"region":"us-east-1"}`, false},
		{"not json", "bearded dragon", false},
		{"bearer on a later line", "x\nAuthorization: Bearer abc.def", true},
		{"pem on a later line", "note\n-----BEGIN CERTIFICATE-----\nMIIE", true},
		{"json on a later line", "note\n{\"api_key\":\"AIza\"}", true},
		{"json api key", `{"type":"service_account","api_key":"AIza"}`, true},
		{"url query signature", "https://cache.example.com/objects?sig=abc", true},
		{"url path secret", "https://hooks.example.com/services/T0/zzzSECRET", true},
		{"plain url", "https://cache.example.com/v1/objects", false},
		{"plain multi-line", "line one\nline two", false},
		{"dsn", "postgres://u:p@db.example/sparkwing", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := CredentialValue(c.value); got != c.want {
			t.Errorf("%s: CredentialValue(%q) = %v, want %v", c.name, c.value, got, c.want)
		}
	}
}

func TestRedactValue(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{
			"postgres", "postgres://sparkwing:hunter2@db.example/sparkwing?sslmode=require",
			"postgres://redacted@db.example/sparkwing?sslmode=require",
		},
		{"postgresql", "postgresql://u:p@db.example:5432/app", "postgresql://redacted@db.example:5432/app"},
		{"mysql", "mysql://u:p@db.example:3306/app", "mysql://redacted@db.example:3306/app"},
		{"redis", "redis://u:p@cache.example:6379/0", "redis://redacted@cache.example:6379/0"},
		{"amqp", "amqp://u:p@broker.example:5672/vhost", "amqp://redacted@broker.example:5672/vhost"},
		{"https", "https://u:p@api.example/v1", "https://redacted@api.example/v1"},
		{"no userinfo", "https://api.example/v1", "https://api.example/v1"},
		{"not a url", "/usr/local/bin:/usr/bin", "/usr/local/bin:/usr/bin"},
		{"plain email", "korey@example.com", "korey@example.com"},
		{
			"query signature", "https://cache.example.com/objects?sig=abc&region=us",
			"https://cache.example.com/objects?sig=redacted&region=us",
		},
		{
			"query token beside userinfo", "https://u:p@api.example/v1?access_token=abc",
			"https://redacted@api.example/v1?access_token=redacted",
		},
		{
			"path secret", "https://hooks.example.com/services/T0/zzzSECRET",
			"https://hooks.example.com/services/T0/redacted",
		},
		{"plain path", "https://cache.example.com/v1/objects", "https://cache.example.com/v1/objects"},
	}
	for _, c := range cases {
		if got := RedactValue(c.value); got != c.want {
			t.Errorf("%s: RedactValue(%q) = %q, want %q", c.name, c.value, got, c.want)
		}
	}
}

func TestCredentialTokenBoundaries(t *testing.T) {
	t.Run("names", func(t *testing.T) {
		cases := []struct {
			name string
			want bool
		}{
			{"MONKEY_MODE", false},
			{"BYPASS_CHECKS", false},
			{"COMPASS_DIR", false},
			{"PASSENGER_COUNT", false},
			{"KEYBOARD_LAYOUT", false},
			{"CERTAINTY", false},
			{"AUTHORITY_NAME", false},
			{"TOKENIZER_PATH", false},
			{"GOOGLE_APPLICATION_CREDENTIALS", true},
			{"AWS_SECRET_ACCESS_KEY", true},
			{"apiKey", true},
			{"serviceAPIKey", true},
			{"registry-auth", true},
		}
		for _, c := range cases {
			if got := CredentialName(c.name); got != c.want {
				t.Errorf("CredentialName(%q) = %v, want %v", c.name, got, c.want)
			}
		}
	})

	t.Run("values", func(t *testing.T) {
		cases := []struct {
			name  string
			value string
			want  bool
		}{
			{"array under a field", `{"creds":[{"token":"ghp_secret"}]}`, true},
			{"top-level array", `[{"api_key":"secret"}]`, true},
			{"array of arrays", `[[{"password":"hunter2"}]]`, true},
			{"array of plain scalars", `["us-east-1","us-west-2"]`, false},
			{"dotenv blob", "GITHUB_TOKEN=ghp_secret\nOTHER=1", true},
			{"single assignment", "AWS_SECRET_ACCESS_KEY=abc123", true},
			{"assignment beside others", "LANG=C GITHUB_TOKEN=ghp_secret", true},
			{"plain assignment blob", "MONKEY_MODE=1\nCOMPASS_DIR=/tmp", false},
			{"url path monkey", "https://x/api/v1/monkey", false},
			{"url path bypass", "https://api.example.com/v1/bypass", false},
			{"empty assignment value", "TOKEN=", false},
		}
		for _, c := range cases {
			if got := CredentialValue(c.value); got != c.want {
				t.Errorf("%s: CredentialValue(%q) = %v, want %v", c.name, c.value, got, c.want)
			}
		}
	})
}
