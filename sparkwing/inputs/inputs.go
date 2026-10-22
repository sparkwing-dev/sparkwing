// Package inputs declares memo inputs for evaluation by the executing host.
package inputs

import "github.com/sparkwing-dev/sparkwing/sparkwing"

type RepoFilesOption func(*sparkwing.MemoInput)

// Ignore excludes matching tracked paths. Basenames match anywhere; trailing slashes match a root prefix; ** spans segments.
func Ignore(patterns ...string) RepoFilesOption {
	return func(input *sparkwing.MemoInput) { input.Patterns = append(input.Patterns, patterns...) }
}

// RepoFiles declares all tracked files, with optional exclusions.
func RepoFiles(options ...RepoFilesOption) sparkwing.MemoInput {
	input := sparkwing.MemoInput{Kind: "repo_files"}
	for _, option := range options {
		option(&input)
	}
	return input
}

// Files declares tracked files matching patterns, using the same rules as Ignore.
func Files(patterns ...string) sparkwing.MemoInput {
	return sparkwing.MemoInput{Kind: "files", Patterns: patterns}
}

// Tree declares regular files recursively, skipping symlinks. Roots must stay inside the checkout.
func Tree(root string) sparkwing.MemoInput { return sparkwing.MemoInput{Kind: "tree", Root: root} }

// Env declares effective job environment names. Unset differs from empty.
func Env(names ...string) sparkwing.MemoInput { return sparkwing.MemoInput{Kind: "env", Names: names} }

// Const declares a nonempty literal string, validated by the host.
func Const(value string) sparkwing.MemoInput {
	return sparkwing.MemoInput{Kind: "const", Value: value}
}

// Parts declares ordered JSON values for host hashing.
func Parts(parts ...any) sparkwing.MemoInput { return sparkwing.MemoInput{Kind: "parts", Parts: parts} }

// Compose combines recipes in order.
func Compose(inputs ...sparkwing.MemoInput) sparkwing.MemoInput {
	return sparkwing.MemoInput{Kind: "compose", Inputs: inputs}
}
