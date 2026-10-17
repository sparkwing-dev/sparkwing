//go:build unix

package controller_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
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
	burstNativeSDKAcceptance(t, burstSDKPipeService)
}

func burstNativeSDKAcceptance(t *testing.T, serve func(*testing.T, http.Handler) (string, []string)) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, plan, err := sdkserve.DefaultOperations.Plan(t.Context(), burstSDKPipelineName, map[string]string{}, nodeproto.RunContext{Pipeline: burstSDKPipelineName})
	if err != nil {
		t.Fatal(err)
	}
	identity := engine.Identity{Version: "v1.2.3", ManifestDigest: "verified", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}}
	for _, placement := range []string{"here", "personal", "cloud"} {
		t.Run(placement, func(t *testing.T) {
			proof := controller.BurstExecutionProof{
				Pipeline: burstSDKPipelineName, Plan: plan, Engine: identity,
				Execute: func(options client.EngineOptions, award controllerwire.ClaimedUnit, owner controllerwire.Controller) {
					entry := &source.Entry{Engine: engine.Resolved{Identity: identity}, Command: []string{executable, "-test.run=^TestBurstSDKNativeChild$", "--"}, Directory: t.TempDir()}
					executeBurstSDKProcess(t, options, award, owner, &burstSDKNativeExecution{
						entry: entry,
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
