package wingd

import "io/fs"

func socketDirFault(fs.FileInfo) string {
	return ""
}

func socketBaseFault(fs.FileInfo) string {
	return ""
}

func socketDirReapable(fs.FileInfo) bool {
	return false
}
