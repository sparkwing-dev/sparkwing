package limits

import "time"

const (
	// MaxRetries bounds application retries under one claim.
	MaxRetries int = 10
	// MaxDisruptions bounds post-start lease-loss disruptions outside application retries; OOM remains ordinary job failure.
	MaxDisruptions int = 3
	// maxJobs bounds jobs in an accepted plan.
	maxJobs int = 1000
	// maxFanOut bounds generated jobs per fan-out.
	maxFanOut int = 256
	// maxLabels bounds labels per job.
	maxLabels int = 32
	// maxPipelineRefs bounds pipeline references per plan.
	maxPipelineRefs int = 32
	// MaxChildDepth bounds a child-run chain, counting the root run as one level.
	MaxChildDepth int = 10
	// MaxAnnotations bounds annotations per run.
	MaxAnnotations int = 1000
	// MaxEvents bounds events per run.
	MaxEvents int = 50000
	// MaxMetricSamples bounds metric samples per job.
	MaxMetricSamples int = 10000
	// maxCleanups bounds cleanups per job.
	maxCleanups int = 16
	// maxCleanupArgs bounds arguments per cleanup.
	maxCleanupArgs int = 64
	// jobIDBytes bounds maximum encoded job identifier.
	jobIDBytes int64 = 200
	// labelBytes bounds maximum label length.
	labelBytes int64 = 128
	// NodeOutputBytes bounds typed output per job or step.
	NodeOutputBytes int64 = 16 << 20
	// runOutputBytes bounds total typed outputs per run.
	runOutputBytes int64 = 1 << 30
	// ErrorBytes bounds UTF-8 head and tail error text after masking.
	ErrorBytes int64 = 8 << 10
	// failureBytes bounds structured failure record.
	failureBytes int64 = 64 << 10
	// AnnotationBytes bounds individual annotation and summary.
	AnnotationBytes int64 = 128 << 10
	// RunAnnotationBytes bounds annotations per run.
	RunAnnotationBytes int64 = 4 << 20
	// EventBytes bounds individual event.
	EventBytes int64 = 256 << 10
	// RunEventBytes bounds events per run.
	RunEventBytes int64 = 64 << 20
	// cleanupDescriptionBytes bounds cleanup description.
	cleanupDescriptionBytes int64 = 256
	// cleanupBodyBytes bounds cleanup body.
	cleanupBodyBytes int64 = 16 << 10
	// JobScratchBytes bounds fixed scratch volume for every runner Job.
	JobScratchBytes int64 = 20 << 30
	// AttemptLogBytes bounds logs per attempt.
	AttemptLogBytes int64 = 64 << 20
	// RetryBackoff bounds maximum application retry backoff.
	RetryBackoff time.Duration = 5 * time.Minute
	// CleanupTimeout bounds cleanup execution budget.
	CleanupTimeout time.Duration = 2 * time.Minute
)
