package chaos

import "github.com/sparkwing-dev/sparkwing/pkg/procgroup"

func ignoreProcessGroupTermination() { procgroup.IgnoreTermination() }
