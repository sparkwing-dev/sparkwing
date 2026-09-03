package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/cluster"
	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestFleetPublicSurfaceStaysSmallAndCredentialFree(t *testing.T) {
	if !slices.Equal(cmdFleet.SubcommandOrder, []string{"init", "agents"}) {
		t.Fatalf("fleet subcommands = %v", cmdFleet.SubcommandOrder)
	}
	if !slices.Equal(cmdFleetAgents.SubcommandOrder, []string{"enroll"}) {
		t.Fatalf("fleet agents subcommands = %v", cmdFleetAgents.SubcommandOrder)
	}
	wantInit := []string{"--allow-tailnet-http", "--listen", "--public-url", "--tailnet"}
	gotInit := flagNames(cmdFleetInit.Flags)
	slices.Sort(gotInit)
	if !slices.Equal(gotInit, wantInit) {
		t.Fatalf("fleet init flags = %v, want %v", gotInit, wantInit)
	}
	wantEnroll := []string{
		"--base-priority", "--budget-cores", "--budget-memory-bytes", "--capability", "--location",
		"--max-concurrent", "--name", "--priority-ceiling", "--ttl",
	}
	gotEnroll := flagNames(cmdFleetAgentsEnroll.Flags)
	slices.Sort(gotEnroll)
	if !slices.Equal(gotEnroll, wantEnroll) {
		t.Fatalf("fleet agents enroll flags = %v, want %v", gotEnroll, wantEnroll)
	}
	for _, help := range []string{cmdFleet.Description, cmdFleetInit.Description, cmdFleetAgents.Description, cmdFleetAgentsEnroll.Description} {
		for _, forbidden := range []string{"--principal", "--kind", "gateway", "token-prefix", "authority_id"} {
			if strings.Contains(help, forbidden) {
				t.Errorf("fleet help exposes %q: %s", forbidden, help)
			}
		}
	}
}

func TestDirectTailnetFleetConfigIsExplicitAndUnambiguous(t *testing.T) {
	cfg, err := directTailnetFleetConfig([]netip.Addr{
		netip.MustParseAddr("fd7a:115c:a1e0::1"), netip.MustParseAddr("100.64.1.2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "100.64.1.2:4346" || cfg.PublicURL != "http://100.64.1.2:4346" || !cfg.AllowTailnetHTTP {
		t.Fatalf("direct tailnet config = %+v", cfg)
	}
	if _, err := directTailnetFleetConfig([]netip.Addr{netip.MustParseAddr("100.64.1.2"), netip.MustParseAddr("100.64.1.3")}); err == nil {
		t.Fatal("ambiguous Tailscale IPv4 addresses were accepted")
	}
}

func TestFleetInitCreatesCredentialFreePolicyAndRefusesReplacement(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "fleet.yaml")
	t.Setenv("SPARKWING_FLEET_CONFIG", configPath)
	output := captureStdout(t, func() {
		if err := runFleetInit([]string{
			"--listen", "127.0.0.1:4346", "--public-url", "https://fleet.example.test",
		}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, configPath) {
		t.Fatalf("fleet init output = %q", output)
	}
	cfg, err := fleet.Load(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:4346" || cfg.PublicURL != "https://fleet.example.test" || len(cfg.Executors) != 0 {
		t.Fatalf("initialized fleet config = %+v", cfg)
	}
	body := string(mustReadFleetFile(t, configPath))
	for _, forbidden := range []string{"token", "prefix", "credential", "principal", "authority_id"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("initialized fleet config exposes %q: %s", forbidden, body)
		}
	}
	if err := runFleetInit([]string{
		"--listen", "127.0.0.1:4346", "--public-url", "https://other.example.test",
	}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("replacement fleet init error = %v", err)
	}
}

func TestFleetAgentsEnrollUpdatesTrustAndPrintsRawOnlyOnce(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "fleet.yaml")
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_FLEET_CONFIG", configPath)
	if err := fleet.Create(configPath, fleet.Config{
		Listen: "127.0.0.1:7443", PublicURL: "http://127.0.0.1:7443",
		Local: fleet.Local{MaxConcurrent: 1, Contribution: "50%,50%"},
	}, nil); err != nil {
		t.Fatal(err)
	}

	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			if err := runFleetAgentsEnroll([]string{"--name", "desk", "--location", "local", "--capability", "toolchain=go"}); err != nil {
				t.Fatal(err)
			}
		})
	})
	var agent struct {
		Coordinators []struct {
			Name, Controller, Logs, Token string
		} `yaml:"coordinators"`
	}
	if err := yaml.Unmarshal([]byte(stdout), &agent); err != nil {
		t.Fatalf("parse one-time agent config: %v\n%s", err, stdout)
	}
	if len(agent.Coordinators) != 1 || agent.Coordinators[0].Name != "desk" {
		t.Fatalf("agent config = %+v", agent)
	}
	raw := agent.Coordinators[0].Token
	if !strings.HasPrefix(raw, "swr_") || strings.Count(stdout, raw) != 1 {
		t.Fatalf("one-time credential output count = %d", strings.Count(stdout, raw))
	}
	if strings.Contains(stderr, raw) {
		t.Fatal("stderr exposed raw credential")
	}
	agentPath := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(agentPath, []byte(stdout), fssecure.FileMode); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.SecurePrivateConfig(agentPath); err != nil {
		t.Fatal(err)
	}
	loadedAgent, err := cluster.LoadAgentConfig(agentPath)
	if err != nil {
		t.Fatalf("load emitted agent.yaml snippet: %v", err)
	}
	normalizedAgent, err := cluster.ValidateAgentConfig(*loadedAgent)
	if err != nil {
		t.Fatalf("validate emitted agent.yaml snippet: %v", err)
	}
	if len(normalizedAgent.Coordinators) != 1 || normalizedAgent.Coordinators[0].Controller != "http://127.0.0.1:7443" ||
		normalizedAgent.Coordinators[0].Logs != "http://127.0.0.1:7443" {
		t.Fatalf("emitted agent membership = %+v", normalizedAgent.Coordinators)
	}

	cfg, err := fleet.Load(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Executors) != 1 || cfg.Executors[0].Name != "desk" || cfg.Executors[0].BasePriority != 50 {
		t.Fatalf("trusted enrollment = %+v", cfg.Executors)
	}
	configBody := string(mustReadFleetFile(t, configPath))
	if strings.Contains(configBody, raw) || strings.Contains(configBody, "token_prefix") || strings.Contains(configBody, "swr_") {
		t.Fatal("fleet config persisted credential identity")
	}

	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	tok, err := st.LookupToken(raw, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	executors, err := st.ListExecutors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(executors) != 1 || executors[0].Name != "desk" || executors[0].TokenPrefix != tok.Prefix || strings.Contains(stdout, tok.Prefix+"\n") {
		t.Fatalf("private credential binding = token %q, executors %+v", tok.Prefix, executors)
	}
	if tok.HasScope(controller.ScopeSecretsRead) || tok.HasScope(controller.ScopeLogsWrite) {
		t.Fatalf("unfenced helper scopes = %v", tok.Scopes)
	}
}

func TestFleetAgentsEnrollRollsBackWhenOneTimeOutputFails(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "fleet.yaml")
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_FLEET_CONFIG", configPath)
	if err := fleet.Create(configPath, fleet.Config{
		Listen: "127.0.0.1:7443", PublicURL: "http://127.0.0.1:7443",
		Local: fleet.Local{MaxConcurrent: 1, Contribution: "50%,50%"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	err := runFleetAgentsEnrollTo([]string{"--name", "desk", "--location", "local"}, failingFleetWriter{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "write one-time agent config") {
		t.Fatalf("output failure = %v", err)
	}
	cfg, err := fleet.Load(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Executors) != 0 {
		t.Fatalf("failed credential delivery changed fleet policy: %+v", cfg.Executors)
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	executors, err := st.ListExecutors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(executors) != 0 {
		t.Fatalf("failed credential delivery left a live enrollment: %+v", executors)
	}
	tokens, err := st.ListTokens(store.TokenKindRunner, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].RevokedAt == nil {
		t.Fatalf("failed credential delivery token rows = %+v", tokens)
	}
}

func TestFleetAgentsEnrollValidatesTrustBeforePrintingCredential(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "fleet.yaml")
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_FLEET_CONFIG", configPath)
	if err := fleet.Create(configPath, fleet.Config{
		Listen: "127.0.0.1:7443", PublicURL: "http://127.0.0.1:7443",
		Local: fleet.Local{MaxConcurrent: 1, Contribution: "50%,50%"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	err := runFleetAgentsEnrollTo([]string{
		"--name", "desk", "--location", "local", "--capability", "location=cloud",
	}, &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "reserved machine or placement key") {
		t.Fatalf("invalid trust error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("invalid trust printed a credential: %q", output.String())
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	executors, err := st.ListExecutors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(executors) != 0 {
		t.Fatalf("invalid trust minted an enrollment: %+v", executors)
	}
}

func TestFleetAgentsEnrollRejectsNegativeCredentialLifetimeBeforeProvisioning(t *testing.T) {
	var output strings.Builder
	err := runFleetAgentsEnrollTo([]string{
		"--name", "desk", "--location", "local", "--ttl", "-1s",
	}, &output, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--ttl must be non-negative") {
		t.Fatalf("negative credential lifetime error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("negative credential lifetime printed a credential: %q", output.String())
	}
}

func TestFleetAgentsEnrollSerializesCredentialAndPolicyCommit(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "fleet.yaml")
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_FLEET_CONFIG", configPath)
	if err := fleet.Create(configPath, fleet.Config{
		Listen: "127.0.0.1:7443", PublicURL: "http://127.0.0.1:7443",
		Local: fleet.Local{MaxConcurrent: 1, Contribution: "50%,50%"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var outputs [2]bytes.Buffer
	var results [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index] = runFleetAgentsEnrollTo([]string{"--name", "desk", "--location", "local"}, &outputs[index], io.Discard)
		}(i)
	}
	close(start)
	wg.Wait()
	succeeded := 0
	for i, err := range results {
		if err == nil {
			succeeded++
			if !strings.Contains(outputs[i].String(), "token: swr_") {
				t.Fatalf("successful enrollment output = %q", outputs[i].String())
			}
		} else if outputs[i].Len() != 0 {
			t.Fatalf("failed enrollment printed a credential: %q (%v)", outputs[i].String(), err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent enrollment results = %v, want one success", results)
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	executors, err := st.ListExecutors(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := st.ListTokens(store.TokenKindRunner, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(executors) != 1 || executors[0].Name != "desk" || len(tokens) != 1 || tokens[0].RevokedAt != nil || tokens[0].Prefix != executors[0].TokenPrefix {
		t.Fatalf("serialized private binding = executors %+v, tokens %+v", executors, tokens)
	}
	cfg, err := fleet.Load(configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Executors) != 1 || cfg.Executors[0].Name != "desk" {
		t.Fatalf("serialized public policy = %+v", cfg.Executors)
	}
}

type failingFleetWriter struct{}

func (failingFleetWriter) Write([]byte) (int, error) { return 0, errors.New("closed output") }

func mustReadFleetFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
