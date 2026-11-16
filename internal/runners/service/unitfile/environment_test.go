package unitfile

import (
	"strings"
	"testing"
)

func TestPinEnvironmentPreservesCapturedSelectors(t *testing.T) {
	for _, tc := range []struct{ goos, body string }{
		{"linux", "[Service]\nExecStart=/bin/true\nEnvironment=SPARKWING_SECRETS_KEY_FILE=/captured.key\n[Install]\nWantedBy=default.target\n"},
		{"darwin", `<plist><dict><key>ProgramArguments</key><array><string>/bin/true</string></array><key>EnvironmentVariables</key><dict><key>SPARKWING_SECRETS_KEY_FILE</key><string>/captured.key</string></dict></dict></plist>`},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			got, err := PinEnvironment(tc.goos, tc.body, "SPARKWING_SECRETS_KEY_FILE", "/ambient.key")
			if err != nil || !strings.Contains(got, "/captured.key") || strings.Contains(got, "/ambient.key") {
				t.Fatalf("lost captured selector: %s, %v", got, err)
			}
		})
	}
}
