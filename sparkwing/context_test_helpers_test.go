package sparkwing

import "context"

func withTestLogger(ctx context.Context, logger Logger) context.Context {
	return context.WithValue(ctx, keyLogger, logger)
}

func withTestNode(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyNode, id)
}

func withTestJSONResolver(ctx context.Context, resolver func(string) ([]byte, bool)) context.Context {
	return context.WithValue(ctx, keyJSONRefResolver, resolver)
}

func withTestRunner(ctx context.Context, runner *RunnerInfo) context.Context {
	return context.WithValue(ctx, runnerCtxKey{}, runner)
}

func withTestInputs(ctx context.Context, inputs any) context.Context {
	return context.WithValue(ctx, keyInputs, inputs)
}
