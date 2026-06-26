package pipelinegen

import (
	"embed"
	"io/fs"
)

//go:embed testdata/corpus
var corpusFS embed.FS

const defaultCorpusRoot = "testdata/corpus"

// DefaultCorpus returns the corpus embedded in the binary and the root
// path to load it from. The same fs.FS backs the fixture generator, so
// each spec's expected source is read from the same place.
func DefaultCorpus() (fs.FS, string) {
	return corpusFS, defaultCorpusRoot
}
