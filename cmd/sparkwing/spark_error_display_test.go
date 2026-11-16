package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestSparkResolverErrorDisplayPreservesRuneBoundariesAndJSON(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/example.com/library/@v/list" {
			t.Errorf("unexpected proxy request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if _, err := io.WriteString(w, "v1.0.0\n"); err != nil {
			t.Error(err)
		}
	}))
	defer proxy.Close()
	t.Setenv("GOPROXY", proxy.URL)
	t.Setenv("GOPRIVATE", "")
	for _, tc := range []struct{ name, input, want string }{
		{"accent split", strings.Repeat("a", 20) + "é" + strings.Repeat("z", 10), "sparks: resolve example.com/library@" + strings.Repeat("a", 20) + "..."},
		{"accent fits", strings.Repeat("a", 19) + "é" + strings.Repeat("z", 10), "sparks: resolve example.com/library@" + strings.Repeat("a", 19) + "é..."},
		{"emoji split", strings.Repeat("a", 20) + "😀" + strings.Repeat("z", 10), "sparks: resolve example.com/library@" + strings.Repeat("a", 20) + "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declared, _ := json.Marshal(tc.input)
			root := writeSparkFixture(t, map[string]string{
				".sparkwing/sparkwing.yaml": "sparks:\n  - name: library\n    source: example.com/library\n    version: " + string(declared) + "\n",
				".sparkwing/go.mod":         "module example.com/fixture\n\ngo 1.26.0\n",
			})
			t.Chdir(root)
			for _, format := range []string{"pretty", "json"} {
				inv, err := clisurface.Parse("sparkwing", []string{"pipeline", "sparks", "list", "-o", format})
				if err != nil {
					t.Fatal(err)
				}
				output := captureStdout(t, func() { err = runSparksList(inv) })
				if err != nil {
					t.Fatal(err)
				}
				if format == "json" {
					lines := strings.Split(strings.TrimSpace(output), "\n")
					if len(lines) != 2 {
						t.Fatalf("records: %q", output)
					}
					var got sparkListLine
					if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
						t.Fatal(err)
					}
					if got.Error != "sparks: resolve example.com/library@"+tc.input+": no version satisfies "+tc.input || got.Declared != tc.input {
						t.Fatalf("JSON changed raw values: %+v", got)
					}
				} else if !utf8.ValidString(output) || !strings.HasSuffix(output, "error: "+tc.want+"\n") {
					t.Fatalf("pretty resolver error: %q; want tail %q", output, "error: "+tc.want)
				}
			}
		})
	}
}
