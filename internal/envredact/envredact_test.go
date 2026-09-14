package envredact

import (
	"bytes"
	"testing"
)

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
		{"PEMBROKE_ROAD", true},
		{"TOKENIZERS_PARALLELISM", false},
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
		{"query apikey", "https://x/y?apikey=abc123", "https://x/y?apikey=redacted"},
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
			{"CERTAINTY", true},
			{"AUTHORITY_NAME", true},
			{"TOKENIZER_PATH", true},
			{"PASSPHRASE", true},
			{"SSH_PASSPHRASE", true},
			{"GPG_PASSPHRASE", true},
			{"CERTFILE", true},
			{"PEMFILE", true},
			{"AUTHHEADER", true},
			{"SECRETFILE", true},
			{"TOKENFILE", true},
			{"PASSWORDFILE", true},
			{"APIKEY", true},
			{"apikey", true},
			{"PASSWD", true},
			{"MYSQL_PASSWD", true},
			{"AUTHORIZATION", true},
			{"HTTP_AUTHORIZATION", true},
			{"AUTHTOKEN", true},
			{"ACCESSTOKEN", true},
			{"PRIVATEKEY", true},
			{"SSH_PRIVATEKEY", true},
			{"CLIENTSECRET", true},
			{"DB_PWD", true},
			{"PWD", false},
			{"OLDPWD", false},
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
			{"concatenated assignment name", "MYSQL_PASSWD=hunter2", true},
			{"prefix assignment name", "SSH_PASSPHRASE=hunter2", true},
			{"assignment beside others", "LANG=C GITHUB_TOKEN=ghp_secret", true},
			{"plain assignment blob", "MONKEY_MODE=1\nCOMPASS_DIR=/tmp", false},
			{"url path monkey", "https://x/api/v1/monkey", false},
			{"url path bypass", "https://api.example.com/v1/bypass", false},
			{"empty assignment value", "TOKEN=", false},
			{"lowercase flag fragment", "ANSIBLE=--extra-vars key=1", false},
			{"allow-listed assignment name", "PWD=/tmp/x\nOLDPWD=/tmp", false},
			{"deny-listed assignment name", "SSH_KEY_DIR=/home/u/.ssh", false},
			{"lowercase pass flag", "--set pass=1", false},
			{"lowercase dotenv-looking flag", "run --opt secret=abc", false},
		}
		for _, c := range cases {
			if got := CredentialValue(c.value); got != c.want {
				t.Errorf("%s: CredentialValue(%q) = %v, want %v", c.name, c.value, got, c.want)
			}
		}
	})
}

func TestCredentialFileName(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".env", true},
		{"services/.env", true},
		{".env.production", true},
		{"staging.env", true},
		{"certs/server.pem", true},
		{"certs/server.key", true},
		{"keystore.p12", true},
		{"android/release.jks", true},
		{"deploy/bundle.pfx", true},
		{".ssh/id_rsa", true},
		{"id_ed25519", true},
		{"credentials", true},
		{"aws/credentials.json", true},
		{"certs/chain.crt", true},
		{"certs/chain.cer", true},
		{"certs/chain.der", true},
		{"config/token.yaml", true},
		{"deploy/secrets.yml", true},
		{".netrc", true},
		{".ssh/id_rsa.pub", false},
		{".env.example", false},
		{"config/prod.env.example", false},
		{"deploy/secrets.yml.template", false},
		{"docs/author.txt", false},
		{"config/authority.yaml", false},
		{"certs/certificates.txt", false},
		{"pkg/controller/auth.go", false},
		{"cmd/sparkwing/secret.go", false},
		{"web/package.json", false},
		{"Makefile", false},
		{"docs/authentication.md", false},
		{"internal/keyring/keyring.rs", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := CredentialFileName(tc.path); got != tc.want {
			t.Errorf("CredentialFileName(%q) = %t, want %t", tc.path, got, tc.want)
		}
	}
}

func TestCredentialFileContent(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
		want    bool
	}{
		{"dotenv assignment", ".env", "APP_NAME=demo\nAPI_TOKEN=live-value\n", true},
		{"private key block", "deploy/bundle.conf", "-----BEGIN RSA PRIVATE KEY-----\nabc\n", true},
		{"ordinary settings", "app.conf", "timeout=30s\nregion=us-west-2\n", false},
		{"json field name with no secret value", "web/package.json", `{"name":"web","private": true,"license":"MIT"}`, false},
		{
			"json service-account key", "deploy/service-account.json",
			"{\n  \"type\": \"service_account\",\n  \"private_key\": \"-----BEGIN PRIVATE KEY-----\\nMIIEv\\n-----END PRIVATE KEY-----\\n\"\n}", true,
		},
		{"json long token value", "deploy/api.json", `{"api_token":"ghp_A1b2C3d4E5f6G7h8I9j0"}`, true},
		{
			"yaml field naming a remote secret", "k8s/external-secret.yaml",
			"spec:\n  data:\n    - secretKey: api-token\n      remoteRef:\n        key: prod/api\n", false,
		},
		{
			"yaml bearer placeholder", "api/openapi.yaml",
			"    description: \"Authorization: Bearer {token}\"\n    bearerFormat: JWT\n", false,
		},
		{
			"yaml embedded secret value", "k8s/secret.yaml",
			"data:\n  password: S3cretValue123456789\n", true,
		},
		{"ini spaced lowercase assignment", "deploy/app.conf", "timeout = 30s\npassword = hunter2\n", true},
		{"dotenv template placeholder", ".env.example", "API_TOKEN=replace-me\n", false},
		{"binary bytes", "fixtures/blob.conf", "\x00\x01API_TOKEN=live-value\n", false},
		{"prose is never read", "notes.txt", "password = hunter2\n", false},
		{"empty file", ".env", "", false},
	}
	for _, tc := range cases {
		if got := CredentialFileContent(tc.path, []byte(tc.content)); got != tc.want {
			t.Errorf("%s: CredentialFileContent = %t, want %t", tc.name, got, tc.want)
		}
	}
	oversize := make([]byte, CredentialFilePrefixBytes*2)
	for i := range oversize {
		oversize[i] = 'a'
	}
	copy(oversize, []byte("API_TOKEN=live-value\n"))
	if !CredentialFileContent(".env", oversize) {
		t.Error("CredentialFileContent missed a credential inside the judged prefix")
	}
	copy(oversize, bytes.Repeat([]byte("a"), CredentialFilePrefixBytes))
	copy(oversize[CredentialFilePrefixBytes:], []byte("\nAPI_TOKEN=live-value\n"))
	if CredentialFileContent(".env", oversize) {
		t.Error("CredentialFileContent read past the judged prefix")
	}
}
