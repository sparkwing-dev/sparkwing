package chaos

import "github.com/sparkwing-dev/sparkwing/internal/procgroup"

func ignoreProcessGroupTermination() { procgroup.IgnoreTermination() }
