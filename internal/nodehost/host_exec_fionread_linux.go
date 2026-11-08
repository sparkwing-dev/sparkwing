package nodehost

import "golang.org/x/sys/unix"

// hack: golang.org/x/sys/unix names FIONREAD TIOCINQ on Linux.
const fionread = unix.TIOCINQ
