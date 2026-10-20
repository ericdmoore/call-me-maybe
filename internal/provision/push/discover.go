package push

import (
	"bufio"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"callmemaybe/internal/policy"
)

// Neighbours reads Linux's local IPv4 neighbour cache. Registered LAN phones
// already exchange SIP traffic with this box, so their MAC/IP mapping is
// present without scanning the network, root access, or parsing SIP ourselves.
// Routed/IPv6 phones retain pull delivery; absence here never changes calling.
func Neighbours() map[string]netip.Addr {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseNeighbours(f)
}

func parseNeighbours(r io.Reader) map[string]netip.Addr {
	found := map[string]netip.Addr{}
	ambiguous := map[string]bool{}
	scanner := bufio.NewScanner(io.LimitReader(r, 1<<20))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 6 {
			continue
		}
		flags, err := strconv.ParseUint(fields[2], 0, 32)
		ip, ipErr := netip.ParseAddr(fields[0])
		mac, validMAC := policy.NormaliseMAC(fields[3])
		if err != nil || flags&2 == 0 || ipErr != nil || !ip.Is4() || !ip.IsPrivate() || !validMAC || mac == "00:00:00:00:00:00" {
			continue
		}
		if previous, ok := found[mac]; ok && previous != ip {
			ambiguous[mac] = true
		}
		found[mac] = ip
	}
	for mac := range ambiguous {
		delete(found, mac)
	}
	return found
}
