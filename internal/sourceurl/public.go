package sourceurl

import "net"

// PublicIP reports whether ip is an address an outbound request from a
// controller may reach: not loopback, private, link-local, multicast,
// unspecified, special-purpose, or an IPv6 form embedding such an IPv4 address.
func PublicIP(ip net.IP) bool { return routableIP(ip) }
