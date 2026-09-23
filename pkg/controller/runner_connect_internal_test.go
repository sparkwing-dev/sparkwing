package controller

import "testing"

func TestRunnerConnectArgsShipsLogsOnlyToAnAnnouncedLogsService(t *testing.T) {
	got := runnerConnectArgs("https://api.example", "https://logs.example/", "box")
	want := "sparkwing-runner runner --controller https://api.example --logs https://logs.example" +
		" --also-claim-triggers --max-claims-before-restart 0 --holder-prefix box"
	if got != want {
		t.Fatalf("runnerConnectArgs = %q\nwant %q", got, want)
	}
	got = runnerConnectArgs("https://api.example", "", "box")
	want = "sparkwing-runner runner --controller https://api.example" +
		" --also-claim-triggers --max-claims-before-restart 0 --holder-prefix box"
	if got != want {
		t.Fatalf("runnerConnectArgs without a logs service = %q\nwant %q", got, want)
	}
}
