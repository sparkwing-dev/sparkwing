package main

func runDocsListArgs(args []string) error {
	return runSparkwing(append([]string{"docs", "list"}, args...))
}

func runDocsReadArgs(args []string) error {
	return runSparkwing(append([]string{"docs", "read"}, args...))
}

func runDocsMigrationsArgs(args []string) error {
	return runSparkwing(append([]string{"docs", "migrations"}, args...))
}

func runCommandsArgs(args []string) error {
	return runSparkwing(append([]string{"commands"}, args...))
}

func runInfoArgs(args []string) error {
	return runSparkwing(append([]string{"info"}, args...))
}

func runVersionArgs(args []string) error {
	return runSparkwing(append([]string{"version"}, args...))
}

func runDocsArgs(args []string) error { return runSparkwing(append([]string{"docs"}, args...)) }

func runDaemonArgs(args []string) error       { return runSparkwing(append([]string{"daemon"}, args...)) }
func runWingdArgs(args []string) error        { return runSparkwing(append([]string{"wingd"}, args...)) }
func dispatchCommandArgs(args []string) error { return runSparkwing(args) }

func runUpdateArgs(args []string) error { return runSparkwing(append([]string{"update"}, args...)) }

func runJobsArgs(args []string) error { return runSparkwing(append([]string{"runs"}, args...)) }

func runCacheInfoArgs(args []string) error {
	return runSparkwing(append([]string{"cache", "info"}, args...))
}

func runCachePruneArgs(args []string) error {
	return runSparkwing(append([]string{"cache", "prune"}, args...))
}

func runCacheExplainArgs(args []string) error {
	return runSparkwing(append([]string{"cache", "explain"}, args...))
}

func runCompletionArgs(args []string) error {
	return runSparkwing(append([]string{"completion"}, args...))
}

func runVersionHoldArgs(args []string) error {
	return runSparkwing(append([]string{"version", "hold"}, args...))
}

func runReposListArgs(args []string) error {
	return runSparkwing(append([]string{"repos", "list"}, args...))
}

func runSparksCatalogArgs(args []string) error {
	return runSparkwing(append([]string{"pipeline", "sparks", "catalog"}, args...))
}

func runSparksLintArgs(args []string) error {
	return runSparkwing(append([]string{"pipeline", "sparks", "lint"}, args...))
}

func runSparksAddArgs(args []string) error {
	return runSparkwing(append([]string{"pipeline", "sparks", "add"}, args...))
}

func runSparksInflateArgs(args []string) error {
	return runSparkwing(append([]string{"pipeline", "sparks", "inflate"}, args...))
}

func runReposUpdateArgs(args []string) error {
	return runSparkwing(append([]string{"repos", "update"}, args...))
}

func runPipelineTriggerArgs(args []string) error {
	return runSparkwing(append([]string{"pipeline", "trigger"}, args...))
}

func runRunnersAddArgs(args []string) error {
	return runSparkwing(append([]string{"runners", "add"}, args...))
}

func runXrepoAddArgs(args []string) error {
	return runSparkwing(append([]string{"repos", "add"}, args...))
}

func runPipelineDescribeArgs(args []string) error {
	return runSparkwing(append([]string{"pipeline", "describe"}, args...))
}

func dispatchRunArgs(args []string) error { return runSparkwing(append([]string{"run"}, args...)) }
