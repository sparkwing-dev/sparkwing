package store

var metricAvailabilityColumns = map[string]string{
	"sample_kind":      "TEXT NOT NULL DEFAULT ''",
	"cpu_available":    "INTEGER NOT NULL DEFAULT 0",
	"memory_available": "INTEGER NOT NULL DEFAULT 0",
}
