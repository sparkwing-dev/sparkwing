package inputs

import "github.com/sparkwing-dev/sparkwing/sparkwing"

// safety: RepoFiles uses tracked content so transient build outputs cannot change a memo key.
func RepoFiles(options ...RepoFilesOption) sparkwing.MemoKeyFn {
	panic("unimplemented: c3-protocol-sdk")
}

// safety: Files hashes matching tracked content and fails on invalid patterns.
func Files(globs ...string) sparkwing.MemoKeyFn { panic("unimplemented: c3-protocol-sdk") }

// safety: Tree hashes regular files without following symlinks outside the requested tree.
func Tree(root string) sparkwing.MemoKeyFn { panic("unimplemented: c3-protocol-sdk") }

// safety: Env distinguishes an unset name from an explicitly empty value.
func Env(names ...string) sparkwing.MemoKeyFn { panic("unimplemented: c3-protocol-sdk") }

// safety: Const permits author-owned stable keys while empty keys remain invalid.
func Const(value string) sparkwing.MemoKeyFn { panic("unimplemented: c3-protocol-sdk") }

// safety: Compose propagates errors and NoMemo without silently hashing invalid input.
func Compose(inputs ...sparkwing.MemoKeyFn) sparkwing.MemoKeyFn {
	panic("unimplemented: c3-protocol-sdk")
}
