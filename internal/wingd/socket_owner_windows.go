package wingd

import "io/fs"

func socketDirOwnedByCurrentUser(fs.FileInfo) bool {
	return false
}
