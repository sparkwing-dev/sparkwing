package userconfig

import "testing"

func TestRunRoutingConfiguration(t *testing.T) {
	cfg, err := Decode("config.yaml", []byte("run:\n  routing:\n    route: cloud\n    profile: work\n    prefer:\n      - target: here\n        hold: 5s\n      - target: machines\n        hold: 10s\n      - target: pool=linux\n    overflow_minutes: 0\n    local_secrets: [TOKEN]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Run.Routing.Route == nil || *cfg.Run.Routing.Route != "cloud" || cfg.Run.Routing.OverflowMinutes == nil || *cfg.Run.Routing.OverflowMinutes != 0 {
		t.Fatalf("routing: %+v", cfg.Run.Routing)
	}
	for _, raw := range []string{"route: wrong", "prefer: [{target: cloud, hold: -1s}]", "prefer: [{target: anything}]", "overflow_minutes: -1", "local_secrets: ['bad name']"} {
		if _, err := Decode("config.yaml", []byte("run:\n  routing: {"+raw+"}\n")); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
