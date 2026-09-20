package orchestrator

import (
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// AdmissionClassEnv carries trigger inference across the CLI/pipeline process
// boundary. Pipeline code can override it with Plan.AdmissionClass.
const AdmissionClassEnv = "SPARKWING_ADMISSION_CLASS"

func admissionClassFromEnv() sparkwing.AdmissionClass {
	switch class := sparkwing.AdmissionClass(os.Getenv(AdmissionClassEnv)); class {
	case sparkwing.AdmissionCritical, sparkwing.AdmissionInteractive,
		sparkwing.AdmissionNormal, sparkwing.AdmissionBatch:
		return class
	default:
		return ""
	}
}

func inferredAdmissionClass(explicit sparkwing.AdmissionClass, triggerSource string) sparkwing.AdmissionClass {
	if explicit != "" {
		return explicit
	}
	source := strings.ToLower(triggerSource)
	switch {
	case strings.Contains(source, "pre-commit"), strings.Contains(source, "pre-push"):
		return sparkwing.AdmissionInteractive
	case strings.Contains(source, "schedule"), strings.Contains(source, "post-commit"),
		strings.HasPrefix(source, "await-pipeline"), source == "retry":
		return sparkwing.AdmissionBatch
	default:
		return sparkwing.AdmissionNormal
	}
}

func effectiveAdmissionClass(plan *sparkwing.Plan, inferred sparkwing.AdmissionClass) sparkwing.AdmissionClass {
	if class := plan.AdmissionClassValue(); class != "" {
		return class
	}
	if inferred != "" {
		return inferred
	}
	return sparkwing.AdmissionNormal
}
