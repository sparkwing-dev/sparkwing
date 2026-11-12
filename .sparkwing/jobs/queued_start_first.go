package jobs

import (
	"cmp"
	"slices"
)

// perf: known long packages start early so a short lane does not finish
// with idle slots waiting for a late integration package.
var queuedStartFirstPackages = [...]string{
	"github.com/sparkwing-dev/sparkwing/cmd/sparkwing",
	"github.com/sparkwing-dev/sparkwing/internal/controller",
	storeTestPackage,
	"sparkwing-pipelines/jobs",
	"github.com/sparkwing-dev/sparkwing/internal/bincache",
	"github.com/sparkwing-dev/sparkwing/tests/acceptance",
}

func startHintedPackagesFirst(lanes []queuedTestLane) {
	priority := func(pkg string) int {
		for i, hint := range queuedStartFirstPackages {
			if pkg == hint {
				return i
			}
		}
		return len(queuedStartFirstPackages)
	}
	for _, lane := range lanes {
		slices.SortStableFunc(lane.packages, func(a, b *queuedTestPackage) int {
			return cmp.Compare(priority(a.pkg), priority(b.pkg))
		})
	}
}
