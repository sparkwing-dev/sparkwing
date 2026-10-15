package nodehost

import (
	"strings"
	"time"
)

const localServiceTimeout = 60 * time.Second

func validNodeID(id string) bool {
	if id == "" || len(id) > 256 || strings.Contains(id, "\\") {
		return false
	}
	for part := range strings.SplitSeq(id, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validName(name string) bool {
	return name != "" && len(name) <= 256 && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}
