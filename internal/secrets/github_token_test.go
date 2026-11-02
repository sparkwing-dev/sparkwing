package secrets

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestGitHubInstallationTokensMaskEveryStreamBoundary(t *testing.T) {
	for _, size := range []int{40, 520, 4096} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			token := "ghs_" + strings.Repeat("aB1_-.", (size-4)/6) + strings.Repeat("z", (size-4)%6)
			if size == 40 {
				token = "ghs_" + strings.Repeat("aB1", 12)
			}
			m := NewMasker()
			if err := m.Register(token); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{token, base64.StdEncoding.EncodeToString([]byte(token)), base64.RawURLEncoding.EncodeToString([]byte(token)), strings.ReplaceAll(token, "_", `\u005f`)} {
				input := "before " + value + " after"
				if m.Mask(input) != "before *** after" {
					t.Fatal("token survived value masking")
				}
				for cut := range len(input) + 1 {
					stream := m.Stream()
					got := stream.Mask(input[:cut]) + stream.Mask(input[cut:]) + stream.Flush()
					if got != "before *** after" {
						t.Fatalf("token survived streaming at cut %d", cut)
					}
				}
			}
		})
	}
}
