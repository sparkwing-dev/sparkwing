//go:build darwin

package nodemetrics

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// hack: CGO-free releases cannot call task_info or proc_pid_rusage, while
// getrusage reports a lifetime peak; ps is the available live RSS source.
func processRSS() (int64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0, false
	}
	return parseProcessRSSKB(string(out))
}
