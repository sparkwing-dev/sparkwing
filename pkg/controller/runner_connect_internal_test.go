package controller

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func TestRunnerConnectArgsShipsLogsOnlyToAnAnnouncedLogsService(t *testing.T) {
	allow, err := sourceurl.ParseRepoAllowlist([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	got := runnerConnectArgs("https://api.example", "https://logs.example/", "box", allow)
	want := "sparkwing-runner runner --controller https://api.example --logs https://logs.example" +
		" --allow-repo 'github.com/acme/*' --also-claim-triggers --max-claims-before-restart 0 --holder-prefix box"
	if got != want {
		t.Fatalf("runnerConnectArgs = %q\nwant %q", got, want)
	}
	got = runnerConnectArgs("https://api.example", "", "box", allow)
	want = "sparkwing-runner runner --controller https://api.example" +
		" --allow-repo 'github.com/acme/*' --also-claim-triggers --max-claims-before-restart 0 --holder-prefix box"
	if got != want {
		t.Fatalf("runnerConnectArgs without a logs service = %q\nwant %q", got, want)
	}
}
