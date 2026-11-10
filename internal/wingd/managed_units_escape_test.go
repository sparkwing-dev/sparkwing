package wingd

import (
	"encoding/xml"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
)

func TestControllerDefinitionKeepsHostilePathsInsideOneValue(t *testing.T) {
	hostile := "/tmp/a b\"c'd\\e%n$HOME\n[Service]\nExecStartPre=/bin/evil\r<key>&]]></string><key>X</key><string>x"
	env := map[string]string{"SPARKWING_SECRETS_KEY_FILE": hostile, "SPARKWING_SECRETS_KEY": ""}
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			root := t.TempDir()
			m := &nativeUnits{host: crontimer.Host{GOOS: goos, Home: root, ConfigHome: root, SparkwingHome: filepath.Join(root, "sw"), PathEnv: "/usr/bin", Env: env}, logPath: filepath.Join(root, "log")}
			body := m.render("/bin/sparkwing", "v1")
			got := map[string]string{}
			if goos == "linux" {
				for _, line := range strings.Split(body, "\n") {
					if strings.HasPrefix(line, "ExecStartPre") {
						t.Fatalf("injected line %q", line)
					}
					word, ok := strings.CutPrefix(line, "Environment=")
					if !ok {
						continue
					}
					word = strings.ReplaceAll(word, "%%", "%")
					if strings.HasPrefix(word, "\"") {
						var err error
						if word, err = strconv.Unquote(word); err != nil {
							t.Fatal(err)
						}
					}
					k, v, _ := strings.Cut(word, "=")
					got[k] = v
				}
			} else {
				var plist struct {
					Dict struct {
						Inner []struct {
							Keys    []string `xml:"key"`
							Strings []string `xml:"string"`
						} `xml:"dict"`
					} `xml:"dict"`
				}
				if err := xml.Unmarshal([]byte(body), &plist); err != nil {
					t.Fatal(err)
				}
				for _, d := range plist.Dict.Inner {
					if len(d.Keys) == 3 {
						for i, k := range d.Keys {
							got[k] = d.Strings[i]
						}
					}
				}
			}
			if goos == "linux" && strings.Count(body, "\n[Service]\n") != 1 {
				t.Fatalf("injected section in\n%s", body)
			}
			if len(got) != 3 || got["SPARKWING_SECRETS_KEY_FILE"] != hostile || got["SPARKWING_SECRETS_KEY"] != "" || got["PATH"] != "/usr/bin" {
				t.Fatalf("decoded environment %q from\n%s", got, body)
			}
		})
	}
}
