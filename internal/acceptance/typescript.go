package acceptance

// TypeScriptTests names the SDK boundary proofs within a larger host scenario.
// Placement, persistence and native Windows proofs still use the target matrix.
func TypeScriptTests(scenario string) []string {
	switch scenario {
	case "protocol-hello-and-handles":
		return []string{"example.test.ts", "transport.test.ts"}
	case "protocol-version-and-hard-cut", "protocol-envelope-transcripts":
		return []string{"protocol-v2.test.ts", "amendments.test.ts"}
	case "protocol-additive-fields-and-child-handles":
		return []string{"protocol-v2.test.ts", "transport.test.ts", "native-package.test.ts", "native-results.test.mjs", "initialization-docs.test.ts"}
	case "protocol-strict-plan-parity":
		return []string{"plan.test.ts", "describe.test.ts", "amendments.test.ts"}
	case "protocol-errors-and-frame-identity":
		return []string{"log.test.ts", "routes.test.ts"}
	case "protocol-sdk-secrets-mask-annotations", "protocol-job-closures-and-skip":
		return []string{"runner.test.ts", "settlement.test.ts", "diagnostics.test.ts"}
	case "protocol-mask-acknowledgement":
		return []string{"runner.test.ts", "routes.test.ts"}
	case "protocol-sdk-artifacts-references-children":
		return []string{"data-services.test.ts"}
	case "protocol-memo-input-boundaries":
		return []string{"files-memo.test.ts"}
	case "protocol-host-layers-risk-cleanup":
		return []string{"command.test.ts", "docker-services.test.ts", "git-helper.test.ts"}
	default:
		return nil
	}
}
