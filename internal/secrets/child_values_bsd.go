//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package secrets

// hack: x/sys/unix does not export FIONREAD, _IOR('f', 127, int), for these
// systems.
const fionread = 0x4004667f
