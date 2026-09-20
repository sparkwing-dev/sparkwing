// Package diskspace reports the free and total bytes of the volume holding a
// path. Usage counts the space available to the calling user, which on a
// volume with reserved blocks is less than the raw free count.
package diskspace
