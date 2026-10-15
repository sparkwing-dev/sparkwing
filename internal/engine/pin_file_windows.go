package engine

import "os"

func openPinFile(path string) (*os.File, error) {
	return os.Open(path)
}
