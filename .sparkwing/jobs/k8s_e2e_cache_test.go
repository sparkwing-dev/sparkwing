package jobs

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUploadedFixtureBuildCacheSurvivesCheckoutPathAndRunsBodies(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executions")
	t.Setenv("SPARKWING_TEST_UPLOADED_FIXTURE_BODY_MARKER", marker)
	var first [sha256.Size]byte
	runs := 0
	for checkoutIndex := range 2 {
		checkout := t.TempDir()
		directory := filepath.Join(checkout, ".sparkwing")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"go.mod": "module example.test/uploaded-fixture\n\ngo 1.26.0\n",
			"proof_test.go": `package fixture
import ("os"; "testing")
func TestProofRuns(t *testing.T) {
 f, err := os.OpenFile(os.Getenv("SPARKWING_TEST_UPLOADED_FIXTURE_BODY_MARKER"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
 if err != nil { t.Fatal(err) }
 if _, err := f.WriteString("ran\n"); err != nil { t.Fatal(err) }
 if err := f.Close(); err != nil { t.Fatal(err) }
}
`,
		} {
			if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		binary := filepath.Join(checkout, "proof.test")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		for range 2 {
			cmd := uploadedFixtureTestCommand(checkout)
			cmd.Args = append([]string{cmd.Args[0], "test", "-o", binary}, cmd.Args[2:]...)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("uploaded proof: %v\n%s", err, output)
			}
			runs++
			body, err := os.ReadFile(marker)
			if err != nil || string(body) != strings.Repeat("ran\n", runs) {
				t.Fatalf("uploaded proof body was replayed instead of run: executions=%q want=%d err=%v", body, runs, err)
			}
		}
		body, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		got := sha256.Sum256(body)
		if checkoutIndex == 0 {
			first = got
		} else if got != first {
			t.Fatalf("test executable still depends on checkout path: first=%x second=%x", first, got)
		}
	}
}
