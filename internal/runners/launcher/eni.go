package launcher

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
)

func validateENI(raw string) error {
	var entries []struct {
		ENIID        string `json:"eniId"`
		IfAddress    string `json:"ifAddress"`
		PrivateIP    string `json:"privateIp"`
		IPv6Addr     string `json:"ipv6Addr"`
		VlanID       int    `json:"vlanId"`
		SubnetCIDR   string `json:"subnetCidr"`
		SubnetV6CIDR string `json:"subnetV6Cidr"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&entries); err != nil {
		return errors.New("Pod has invalid ENI annotation")
	}
	if decoder.Decode(new(any)) != io.EOF || len(entries) != 1 {
		return errors.New("Pod requires one ENI annotation entry")
	}
	e := entries[0]
	id, ok := strings.CutPrefix(e.ENIID, "eni-")
	if !ok || (len(id) != 8 && len(id) != 17) || strings.Trim(id, "0123456789abcdef") != "" {
		return errors.New("Pod has invalid ENI identity")
	}
	mac, err := net.ParseMAC(e.IfAddress)
	if err != nil || len(mac) != 6 || e.VlanID < 1 || e.VlanID > 4094 {
		return errors.New("Pod has invalid ENI link")
	}
	if e.PrivateIP == "" && e.IPv6Addr == "" {
		return errors.New("Pod ENI has no address")
	}
	for _, pair := range []struct {
		address, prefix string
		v6              bool
	}{{e.PrivateIP, e.SubnetCIDR, false}, {e.IPv6Addr, e.SubnetV6CIDR, true}} {
		if pair.address == "" && pair.prefix == "" {
			continue
		}
		address, err := netip.ParseAddr(pair.address)
		prefix, prefixErr := netip.ParsePrefix(pair.prefix)
		if err != nil || prefixErr != nil || address.Is6() != pair.v6 || !prefix.Contains(address) {
			return errors.New("Pod ENI address does not match its subnet")
		}
	}
	return nil
}
