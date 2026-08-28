package pipelinegen

import (
	"embed"
	"io/fs"
)

//go:embed testdata/corpus
var corpusFS embed.FS

const defaultCorpusRoot = "testdata/corpus"

func DefaultCorpus() (fs.FS, string) {
	return corpusFS, defaultCorpusRoot
}
