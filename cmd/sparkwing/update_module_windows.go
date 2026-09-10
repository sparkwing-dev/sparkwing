package main

import "os"

func openUpdateInput(path string) (*os.File, error) {
	return os.Open(path)
}
