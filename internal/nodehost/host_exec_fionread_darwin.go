package nodehost

// hack: golang.org/x/sys/unix omits FIONREAD on darwin; this is its _IOR('f', 127, int).
const fionread = 0x4004667f
