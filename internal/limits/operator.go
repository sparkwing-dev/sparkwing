package limits

import "time"

const (
	// LocalControllerHandshake supplies single local controller handshake budget.
	LocalControllerHandshake time.Duration = time.Minute
	// RunnerConcurrency supplies default configured runner concurrency.
	RunnerConcurrency int = 1
	// OperatorEnvMaskMinLength supplies minimum remote allow-listed value length registered for masking.
	OperatorEnvMaskMinLength int = 12
)
