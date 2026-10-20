package limits

import "time"

const (
	// MaxRetries bounds application retries under one claim.
	MaxRetries int = 10
	// MaxDisruptions bounds post-start lease-loss disruptions outside application retries; OOM remains ordinary job failure.
	MaxDisruptions int = 3
	// MaxJobs bounds jobs in an accepted plan.
	MaxJobs int = 1000
	// MaxFanOut bounds generated jobs per fan-out.
	MaxFanOut int = 256
	// MaxLabels bounds labels per job.
	MaxLabels int = 32
	// MaxPipelineRefs bounds pipeline references per plan.
	MaxPipelineRefs int = 32
	// MaxChildDepth bounds a child-run chain, counting the root run as one level.
	MaxChildDepth int = 10
	// MaxChildrenPerRoot bounds the child runs started under one root run, at every depth.
	MaxChildrenPerRoot int = 50
	// MaxAnnotations bounds annotations per run.
	MaxAnnotations int = 1000
	// MaxEvents bounds events per run.
	MaxEvents int = 50000
	// MaxMetricSamples bounds metric samples per job.
	MaxMetricSamples int = 10000
	// MaxCleanups bounds cleanups per job.
	MaxCleanups int = 16
	// MaxCleanupArgs bounds arguments per cleanup.
	MaxCleanupArgs int = 64
	// JobIDBytes bounds maximum encoded job identifier.
	JobIDBytes int64 = 200
	// LabelBytes bounds maximum label length.
	LabelBytes int64 = 128
	// NodeOutputBytes bounds typed output per job or step.
	NodeOutputBytes int64 = 16 << 20
	// RunOutputBytes bounds total typed outputs per run.
	RunOutputBytes int64 = 1 << 30
	// ErrorBytes bounds UTF-8 head and tail error text after masking.
	ErrorBytes int64 = 8 << 10
	// FailureBytes bounds structured failure record.
	FailureBytes int64 = 64 << 10
	// AnnotationBytes bounds individual annotation and summary.
	AnnotationBytes int64 = 128 << 10
	// RunAnnotationBytes bounds annotations per run.
	RunAnnotationBytes int64 = 4 << 20
	// EventBytes bounds individual event.
	EventBytes int64 = 256 << 10
	// RunEventBytes bounds events per run.
	RunEventBytes int64 = 64 << 20
	// CleanupDescriptionBytes bounds cleanup description.
	CleanupDescriptionBytes int64 = 256
	// CleanupBodyBytes bounds cleanup body.
	CleanupBodyBytes int64 = 16 << 10
	// JobScratchBytes bounds fixed scratch volume for every runner Job.
	JobScratchBytes int64 = 20 << 30
	// AttemptLogBytes bounds logs per attempt.
	AttemptLogBytes int64 = 64 << 20
	// RetryBackoff bounds maximum application retry backoff.
	RetryBackoff time.Duration = 5 * time.Minute
	// CleanupTimeout bounds cleanup execution budget.
	CleanupTimeout time.Duration = 2 * time.Minute
)
