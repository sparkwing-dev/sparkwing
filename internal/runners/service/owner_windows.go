package service

import "os"

func fileOwner(os.FileInfo) (int, bool) { return 0, false }
