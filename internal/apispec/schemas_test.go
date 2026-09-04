package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeTypes = `package fake

type Sample struct {
	Name    string ` + "`json:\"name\"`" + `
	TTLSecs int64  ` + "`json:\"ttl_secs,omitempty\"`" + `
	Hidden  string ` + "`json:\"-\"`" + `
}
`

func fakeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "fake")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(fakeTypes), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

func specWithSchema(body string) string {
	return "openapi: 3.0.3\ncomponents:\n  schemas:\n    Sample:\n" + body
}

const matchingSample = `      x-sparkwing-go-type: fake.Sample
      type: object
      properties:
        name: {type: string}
        ttl_secs: {type: integer, format: int64}
`

func TestSchemaCheckAcceptsASchemaThatMatchesItsGoType(t *testing.T) {
	if err := checkSchemaDrift(specWithSchema(matchingSample), fakeRoot(t)); err != nil {
		t.Fatalf("checkSchemaDrift: %v", err)
	}
}

func TestSchemaCheckRejectsDrift(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "member the Go type does not serialize",
			body: `      x-sparkwing-go-type: fake.Sample
      type: object
      properties:
        name: {type: string}
        ttl_secs: {type: integer}
        plan_admission: {type: object}
`,
			want: `documents "plan_admission", which fake.Sample does not serialize`,
		},
		{
			name: "member the schema drops",
			body: `      x-sparkwing-go-type: fake.Sample
      type: object
      properties:
        name: {type: string}
`,
			want: `omits "ttl_secs", which fake.Sample serializes`,
		},
		{
			name: "member documented as the wrong type",
			body: `      x-sparkwing-go-type: fake.Sample
      type: object
      properties:
        name: {type: string}
        ttl_secs: {type: string, format: date-time}
`,
			want: `documents "ttl_secs" as string`,
		},
		{
			name: "object naming no Go type",
			body: `      type: object
      properties:
        name: {type: string}
`,
			want: "no x-sparkwing-go-type",
		},
		{
			name: "object naming a type that does not exist",
			body: `      x-sparkwing-go-type: fake.Missing
      type: object
      properties:
        name: {type: string}
`,
			want: "fake declares no struct type Missing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSchemaDrift(specWithSchema(tc.body), fakeRoot(t))
			if err == nil {
				t.Fatalf("checkSchemaDrift accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkSchemaDrift = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestSchemaCheckSkipsAShapeDeclaredHandBuilt(t *testing.T) {
	body := `      x-sparkwing-go-type: none
      type: object
      properties:
        anything: {type: string}
`
	if err := checkSchemaDrift(specWithSchema(body), fakeRoot(t)); err != nil {
		t.Fatalf("checkSchemaDrift: %v", err)
	}
}

func TestSchemaCheckReachesNestedObjectsAndArrayItems(t *testing.T) {
	body := `      x-sparkwing-go-type: fake.Sample
      type: object
      properties:
        name: {type: string}
        ttl_secs: {type: integer}
        nested:
          type: object
          properties:
            whatever: {type: string}
`
	err := checkSchemaDrift(specWithSchema(body), fakeRoot(t))
	if err == nil {
		t.Fatal("checkSchemaDrift accepted an undeclared nested object")
	}
	if !strings.Contains(err.Error(), "Sample.nested: object has properties but no x-sparkwing-go-type") {
		t.Fatalf("checkSchemaDrift = %v, want it to name the nested object", err)
	}

	body = `      x-sparkwing-go-type: fake.Sample
      type: object
      properties:
        name: {type: string}
        ttl_secs: {type: integer}
        rows:
          type: array
          items:
            type: object
            properties:
              whatever: {type: string}
`
	err = checkSchemaDrift(specWithSchema(body), fakeRoot(t))
	if err == nil {
		t.Fatal("checkSchemaDrift accepted an undeclared array item object")
	}
	if !strings.Contains(err.Error(), "Sample.rows[]: object has properties but no x-sparkwing-go-type") {
		t.Fatalf("checkSchemaDrift = %v, want it to name the array item object", err)
	}
}

func TestCommittedSpecMatchesTheGoTypesItNames(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	if err := checkSchemaDrift(string(body), root); err != nil {
		t.Errorf("api/openapi.yaml has drifted from the controller's wire types:\n%v", err)
	}
}
