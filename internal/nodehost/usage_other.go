//go:build !unix

package nodehost

import "os"

func maxRSSBytes(*os.ProcessState) int64 { return 0 }
