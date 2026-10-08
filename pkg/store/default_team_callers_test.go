package store

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const modulePath = "github.com/sparkwing-dev/sparkwing"

const (
	defaultTeamCli      = "a local CLI command over the laptop's own state database, where every run is created in the default team"
	defaultTeamCrons    = "local workspace crons launch runs into the laptop's state database, in the default team"
	defaultTeamState    = "the laptop and admission-daemon state backend, whose runs and triggers are created through Store.CreateRun and the unbound local claim, so every row it touches is the default team's"
	defaultTeamConc     = "local concurrency runs in-process for the laptop's runs, which are the default team's"
	defaultTeamFleet    = "the local fleet coordinator drives the laptop's own runs, which are the default team's"
	defaultTeamConsumer = "the local trigger consumer claims through ClaimNextTrigger, whose unbound claimant reaches only the default team's queue, so every trigger it settles is the default team's"
	defaultTeamWorktree = "ref worktrees and submission environments exist only for triggers the local consumer claimed, which are the default team's"
	defaultTeamStores   = "finds and reads runs in the laptop's own state databases, where every run is the default team's"
	defaultTeamReplay   = "replays a run into the laptop's state database, which records it in the default team"
	defaultTeamView     = "a local run view over the laptop's state database, where every run is the default team's"
	defaultTeamBackend  = "the store backend serves one install's runs in the default team, as its ListRuns already does; a multi-team deployment serves its dashboard through the controller backend"
	defaultTeamSecrets  = "local secrets live in the laptop's state database under the default team"
	defaultTeamProfiles = "the host's capacity profiles are recorded by its local runs, in the default team"
	defaultTeamHosted   = "the engine-hosted spike creates its run in a private local store through Store.CreateRun, so every node it starts or reads is the default team's"
)

// safety: every entry names a function outside this package that reaches a
// Store method acting on the default team only, with the reason the default
// team is the one it means; a call nobody vouched for is how another team's
// rows go unread.
var defaultTeamCallers = map[string]string{
	"cmd/sparkwing.(cronLauncher).Active":                                   defaultTeamCrons,
	"cmd/sparkwing.(storeBouncer).RequestNodeBounce":                        defaultTeamCli,
	"cmd/sparkwing.addLocalAnnotation":                                      defaultTeamCli,
	"cmd/sparkwing.cancelQueuedLocalRuns":                                   defaultTeamCli,
	"cmd/sparkwing.existingSubmissionResult":                                defaultTeamCli,
	"cmd/sparkwing.failureRowFor":                                           defaultTeamCli,
	"cmd/sparkwing.findExistingSubmission":                                  defaultTeamCli,
	"cmd/sparkwing.listLocalAnnotations":                                    defaultTeamCli,
	"cmd/sparkwing.listLocalApprovals":                                      defaultTeamCli,
	"cmd/sparkwing.persistSubmission":                                       defaultTeamCli,
	"cmd/sparkwing.resetNamedProfile":                                       defaultTeamCli,
	"cmd/sparkwing.resolveLocalApproval":                                    defaultTeamCli,
	"cmd/sparkwing.retryLocalRuns":                                          defaultTeamCli,
	"cmd/sparkwing.runCapacityReset":                                        defaultTeamCli,
	"cmd/sparkwing.runCapacityStats":                                        defaultTeamCli,
	"cmd/sparkwing.runCronsShow":                                            defaultTeamCrons,
	"cmd/sparkwing.runDebugEnv":                                             defaultTeamCli,
	"cmd/sparkwing.runDebugRelease":                                         defaultTeamCli,
	"cmd/sparkwing.runDebugRerunLocal":                                      defaultTeamCli,
	"cmd/sparkwing.renderRunReceipt":                                        defaultTeamCli,
	"cmd/sparkwing.renderRunTree":                                           defaultTeamCli,
	"internal/backend.(*StoreBackend).GetRun":                               defaultTeamBackend,
	"internal/backend.(*StoreBackend).ListEventsAfter":                      defaultTeamBackend,
	"internal/backend.(*StoreBackend).ListNodes":                            defaultTeamBackend,
	"internal/localsecrets.(*storeSource).read":                             defaultTeamSecrets,
	"internal/localsecrets.ImportLegacy":                                    defaultTeamSecrets,
	"internal/opsview.diagnosePoisonedProfiles":                             defaultTeamProfiles,
	"internal/orchestrator.hostedFinalize":                                  defaultTeamHosted,
	"internal/orchestrator.hostedRunNode":                                   defaultTeamHosted,
	"internal/orchestrator.(*HeldRunStore).FinalizeCancelledRuns":           defaultTeamStores,
	"internal/orchestrator.(*HeldRunStore).IsRunTerminal":                   defaultTeamStores,
	"internal/orchestrator.(*HeldRunStore).finalizeRun":                     defaultTeamStores,
	"internal/orchestrator.(*StandaloneStores).Find":                        defaultTeamStores,
	"internal/orchestrator.(consumerRuntime).shouldExit":                    defaultTeamConsumer,
	"internal/orchestrator.(localConcurrency).AcquireSlot":                  defaultTeamConc,
	"internal/orchestrator.(localConcurrency).CancelWaiter":                 defaultTeamConc,
	"internal/orchestrator.(localConcurrency).ForceReleaseSuperseded":       defaultTeamConc,
	"internal/orchestrator.(localConcurrency).HeartbeatSlot":                defaultTeamConc,
	"internal/orchestrator.(localConcurrency).ObserveSlot":                  defaultTeamConc,
	"internal/orchestrator.(localConcurrency).ReleaseSlot":                  defaultTeamConc,
	"internal/orchestrator.(localConcurrency).ResolveWaiter":                defaultTeamConc,
	"internal/orchestrator.(localConcurrency).State":                        defaultTeamConc,
	"internal/orchestrator.(localState).AcknowledgeNodeExecutionStart":      defaultTeamState,
	"internal/orchestrator.(localState).AddNodeMetricSample":                defaultTeamState,
	"internal/orchestrator.(localState).AddNodeUsage":                       defaultTeamState,
	"internal/orchestrator.(localState).AppendNodeAnnotation":               defaultTeamState,
	"internal/orchestrator.(localState).AppendStepAnnotation":               defaultTeamState,
	"internal/orchestrator.(localState).CreateApproval":                     defaultTeamState,
	"internal/orchestrator.(localState).CreateDebugPause":                   defaultTeamState,
	"internal/orchestrator.(localState).EnqueueTriggerWithEnv":              defaultTeamState,
	"internal/orchestrator.(localState).FindSpawnedChildTriggerID":          defaultTeamState,
	"internal/orchestrator.(localState).FinishNodeCopyingOutput":            defaultTeamState,
	"internal/orchestrator.(localState).FinishNodeExecutionAttempt":         defaultTeamState,
	"internal/orchestrator.(localState).FinishNodeStep":                     defaultTeamState,
	"internal/orchestrator.(localState).FinishTrigger":                      defaultTeamState,
	"internal/orchestrator.(localState).GetActiveDebugPause":                defaultTeamState,
	"internal/orchestrator.(localState).GetApproval":                        defaultTeamState,
	"internal/orchestrator.(localState).GetNode":                            defaultTeamState,
	"internal/orchestrator.(localState).GetNodeDispatch":                    defaultTeamState,
	"internal/orchestrator.(localState).GetPipelineProfile":                 defaultTeamState,
	"internal/orchestrator.(localState).GetRun":                             defaultTeamState,
	"internal/orchestrator.(localState).GetTrigger":                         defaultTeamState,
	"internal/orchestrator.(localState).ListDebugPauses":                    defaultTeamState,
	"internal/orchestrator.(localState).ListNodeDispatches":                 defaultTeamState,
	"internal/orchestrator.(localState).ListNodeMetrics":                    defaultTeamState,
	"internal/orchestrator.(localState).ListNodeSteps":                      defaultTeamState,
	"internal/orchestrator.(localState).ListNodes":                          defaultTeamState,
	"internal/orchestrator.(localState).ListPendingApprovals":               defaultTeamState,
	"internal/orchestrator.(localState).ListPendingTriggersForParent":       defaultTeamState,
	"internal/orchestrator.(localState).RecordContention":                   defaultTeamState,
	"internal/orchestrator.(localState).RecordProfileObservation":           defaultTeamState,
	"internal/orchestrator.(localState).RecordWaitObservation":              defaultTeamState,
	"internal/orchestrator.(localState).ReleaseClaimAtGeneration":           defaultTeamState,
	"internal/orchestrator.(localState).ReleaseDebugPause":                  defaultTeamState,
	"internal/orchestrator.(localState).ResetNodeForAutoRetry":              defaultTeamState,
	"internal/orchestrator.(localState).ResolveApproval":                    defaultTeamState,
	"internal/orchestrator.(localState).SetNodeArtifactManifest":            defaultTeamState,
	"internal/orchestrator.(localState).SetNodeStatus":                      defaultTeamState,
	"internal/orchestrator.(localState).SetNodeSummary":                     defaultTeamState,
	"internal/orchestrator.(localState).SetPipelinePin":                     defaultTeamState,
	"internal/orchestrator.(localState).SetStepSummary":                     defaultTeamState,
	"internal/orchestrator.(localState).SkipNodeStep":                       defaultTeamState,
	"internal/orchestrator.(localState).StartNode":                          defaultTeamState,
	"internal/orchestrator.(localState).StartNodeStep":                      defaultTeamState,
	"internal/orchestrator.(localState).TouchNodeHeartbeat":                 defaultTeamState,
	"internal/orchestrator.(localState).TouchRunHeartbeat":                  defaultTeamState,
	"internal/orchestrator.(localState).UpdateNodeActivity":                 defaultTeamState,
	"internal/orchestrator.(localState).UpdateNodeDeps":                     defaultTeamState,
	"internal/orchestrator.(localState).WriteNodeDispatch":                  defaultTeamState,
	"internal/orchestrator.(localStoreFleetCoordinator).GetNode":            defaultTeamFleet,
	"internal/orchestrator.(localStoreFleetCoordinator).ListNodes":          defaultTeamFleet,
	"internal/orchestrator.(localStoreFleetCoordinator).RevokeNodeReady":    defaultTeamFleet,
	"internal/orchestrator.(localStoreFleetCoordinator).TouchNodeHeartbeat": defaultTeamFleet,
	"internal/orchestrator.(localStoreFleetCoordinator).UpdateNodeActivity": defaultTeamFleet,
	"internal/orchestrator.JobErrors":                                       defaultTeamView,
	"internal/orchestrator.JobLogs":                                         defaultTeamView,
	"internal/orchestrator.JobStatus":                                       defaultTeamView,
	"internal/orchestrator.MintReplayRun":                                   defaultTeamReplay,
	"internal/orchestrator.OpenStoreForRun":                                 defaultTeamStores,
	"internal/orchestrator.OpenStoreForRunWrite":                            defaultTeamStores,
	"internal/orchestrator.ReconcileSubmissionEnvironments":                 defaultTeamWorktree,
	"internal/orchestrator.RunSummaryLocal":                                 defaultTeamView,
	"internal/orchestrator.RunTimeline":                                     defaultTeamView,
	"internal/orchestrator.cancelClaimedTriggerIfRequested":                 defaultTeamConsumer,
	"internal/orchestrator.cleanupRefWorktree":                              defaultTeamWorktree,
	"internal/orchestrator.finishCancelledClaimedTrigger":                   defaultTeamConsumer,
	"internal/orchestrator.finishClaimedTriggerFailure":                     defaultTeamConsumer,
	"internal/orchestrator.followFromEnvelope":                              defaultTeamView,
	"internal/orchestrator.followLogs":                                      defaultTeamView,
	"internal/orchestrator.refWorktreeTriggerSettled":                       defaultTeamWorktree,
	"internal/orchestrator.renderStatus":                                    defaultTeamView,
	"internal/orchestrator.runningNodeDetail":                               defaultTeamView,
	"internal/orchestrator.scanLocalRuns":                                   defaultTeamView,
	"internal/orchestrator.settleClaimedTriggerDispatch":                    defaultTeamConsumer,
	"internal/orchestrator.sideloadDispatch":                                defaultTeamReplay,
	"internal/orchestrator.sideloadNode":                                    defaultTeamReplay,
	"internal/orchestrator.sideloadRun":                                     defaultTeamReplay,
	"internal/orchestrator.writeLogsTreeLocal":                              defaultTeamView,
	"internal/orchestrator.writeRunDetailJSON":                              defaultTeamView,
	"pkg/localws.cronRunStatus":                                             defaultTeamCrons,
}

// The default-team twins are what keep the laptop store and the local paths
// working without a team; anything that can hold another team's rows must go
// through that team's handle instead. This reads every package of the module
// with types, so a call is caught whatever the variable holding the store is
// named, and refuses one that no entry vouches for.
func TestDefaultTeamTwinsAreCalledOnlyWhereTheDefaultTeamIsMeant(t *testing.T) {
	twins := defaultTeamTwinNames(t)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  root,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) < 50 {
		t.Fatalf("loaded only %d packages; the check no longer reads the module", len(pkgs))
	}
	seen := map[string]bool{}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Fatalf("load %s: %v", p.PkgPath, e)
		}
		if p.PkgPath == modulePath+"/pkg/store" {
			continue
		}
		for _, f := range p.Syntax {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				key := strings.TrimPrefix(p.PkgPath, modulePath+"/") + "." + funcKey(fn)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok || !twins[sel.Sel.Name] {
						return true
					}
					obj, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
					if !ok || !onStore(obj) {
						return true
					}
					seen[key] = true
					if _, vouched := defaultTeamCallers[key]; !vouched {
						t.Errorf("%s: %s calls Store.%s, which acts on the default team only; "+
							"go through the handle of the team that owns the row, or add %q to defaultTeamCallers "+
							"with the reason the default team is meant", p.Fset.Position(sel.Pos()), key, sel.Sel.Name, key)
					}
					return true
				})
			}
		}
	}
	for key, why := range defaultTeamCallers {
		if strings.TrimSpace(why) == "" {
			t.Errorf("defaultTeamCallers vouches for %q with no reason", key)
		}
		if !seen[key] {
			t.Errorf("defaultTeamCallers vouches for %q, which no longer calls a default-team twin; delete the entry", key)
		}
	}
}

func onStore(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || fn.Pkg() == nil || fn.Pkg().Path() != modulePath+"/pkg/store" {
		return false
	}
	ptr, ok := sig.Recv().Type().(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	return ok && named.Obj().Name() == "Store"
}

func defaultTeamTwinNames(t *testing.T) map[string]bool {
	t.Helper()
	twins := map[string]bool{}
	for _, f := range storeSourceFiles(t) {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv != nil && exprString(fn.Recv.List[0].Type) == "*Store" && delegatesToDefaultTeam(fn) {
				twins[fn.Name.Name] = true
			}
		}
	}
	if len(twins) < 50 {
		t.Fatalf("found only %d default-team twins; the check no longer reads this package", len(twins))
	}
	return twins
}
