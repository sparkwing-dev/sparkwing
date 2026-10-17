package wingwire

import (
	"strings"
	"testing"
)

func FuzzRemoteAdmissionIdentitySeparatesControllers(f *testing.F) {
	f.Add("https://one.example", "run", "https://two.example", "run", "job")
	f.Add("a/node-host/b", "c", "a", "b/node-host/c", "x/y")
	f.Fuzz(func(t *testing.T, first, run, second, otherRun, job string) {
		if strings.TrimRight(first, "/") == "" || strings.TrimRight(second, "/") == "" || run == "" || otherRun == "" {
			return
		}
		one, two := RemoteRunID(first, run), RemoteRunID(second, otherRun)
		equal := strings.TrimRight(first, "/") == strings.TrimRight(second, "/") && run == otherRun
		if (one == two) != equal || !one.Remote() || !two.Remote() || !NodeHostParticipant(string(one), job).RunID().Remote() || NodeSemaphoreParticipant(string(one), job).RunID() != one {
			t.Fatalf("remote admission lost its controller identity: %q %q", one, two)
		}
	})
}
