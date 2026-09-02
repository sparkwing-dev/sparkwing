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
	}
	for _, c := range cases {
		if got := RedactValue(c.value); got != c.want {
			t.Errorf("%s: RedactValue(%q) = %q, want %q", c.name, c.value, got, c.want)
		}
	}
}
