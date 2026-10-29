//go:build unix

package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestBurstSDKNativeChild(t *testing.T) {
	if len(os.Args) < 5 || os.Args[len(os.Args)-2] != "burst-sdk-child" {
		return
	}
	if len(os.Args) == 7 {
		http.DefaultTransport = openBurstSDKPipeTransport(t, os.Args[3], os.Args[4])
	}
	requests, frames := os.NewFile(3, "requests"), os.NewFile(4, "frames")
	syscall.CloseOnExec(3)
	syscall.CloseOnExec(4)
	if err := sdkserve.Serve(context.Background(), requests, frames, sdkserve.Config{SDKVersion: "v1.2.3", Operations: sdkserve.DefaultOperations}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestBurstNativeSDKExecutesHerePersonalAndCloudAwards(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 3.8s measured in the short class; the full suite runs it")
	}
	burstNativeSDKAcceptance(t, burstSDKPipeService)
}

func FuzzRequesterSecretsMaskedThroughNativeExecution(f *testing.F) {
	f.Add([]byte("multi\nline\r\nvalue"))
	f.Add([]byte("quoted\"\\+/%= value"))
	f.Add([]byte{0, 255, 128, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256 {
			return
		}
		value := "requester-private-" + strings.ToValidUTF8(string(data), "�")
		burstNativeSDKSecretAcceptance(t, burstSDKPipeService, value, []string{"personal"})
	})
}

func burstNativeSDKAcceptance(t *testing.T, serve func(*testing.T, http.Handler) (string, []string)) {
	burstNativeSDKSecretAcceptance(t, serve, "", []string{"here", "personal", "cloud"})
}

func burstNativeSDKSecretAcceptance(t *testing.T, serve func(*testing.T, http.Handler) (string, []string), secret string, placements []string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := sdkserve.DefaultOperations.Plan(t.Context(), burstSDKPipelineName, map[string]string{}, nodeproto.RunContext{Pipeline: burstSDKPipelineName})
	if err != nil {
		t.Fatal(err)
	}
	identity := engineid.Identity{Version: "v1.2.3", ManifestDigest: "verified", Pin: engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3"}}
	for _, placement := range placements {
		t.Run(placement, func(t *testing.T) {
			proof := controller.BurstExecutionProof{
				Pipeline: burstSDKPipelineName, Plan: plan, Engine: identity, Secret: secret,
				Execute: func(options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller) {
					entry := &source.Entry{Engine: engine.Resolved{Identity: identity}, Command: []string{executable, "-test.run=^TestBurstSDKNativeChild$", "--"}, Directory: t.TempDir()}
					executeBurstSDKProcess(t, options, award, owner, &burstSDKNativeExecution{
						entry: entry, secretValue: secret,
						serviceURL: func(handler http.Handler) string {
							url, args := serve(t, handler)
							entry.Command = append(append(entry.Command, args...), "burst-sdk-child")
							return url
						},
					})
				},
			}
			if placement == "cloud" {
				configureBurstCloudJob(t, &proof)
			}
			controller.BurstExecutionAcceptance(t, placement, proof)
		})
	}
}
