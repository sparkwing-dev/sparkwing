package fleet

import (
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func verifyPrivateFile(path string, info os.FileInfo) error {
	return fssecure.VerifyPrivateConfig(path, info)
}

func securePrivateFile(path string) error { return fssecure.SecurePrivateConfig(path) }
