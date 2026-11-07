//go:build unix && !linux && !darwin

package nodehost

// hack: other Unix hosts skip the buffered-byte check, so a slow sink can still cut their drain short.
const fionread = 0
