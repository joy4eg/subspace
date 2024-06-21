package main

import (
	"net"

	"github.com/pkg/errors"
)

var (
	ErrIPLimitExceeds = errors.New("num of devices exceeds the limit of IP addr pool")
	ErrInvalidCIDR    = errors.New("invalid CIDR")
	ErrInvalidID      = errors.New("invalid ID")
)

func cloneIP(ip net.IP) net.IP {
	dup := make(net.IP, len(ip))
	copy(dup, ip)
	return dup
}

func calcDefaultGateway(cidr string) (net.IP, *net.IPNet, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, nil, err
	}

	if ones, bits := network.Mask.Size(); bits-ones == 0 {
		return nil, nil, errors.Wrapf(ErrInvalidCIDR, "%v", cidr)
	}

	gwIP := cloneIP(network.IP)
	gwIP[len(gwIP)-1] |= 1

	return gwIP, network, nil
}

// broadcastAddr4 returns the last address in the given network, or the broadcast address.
func broadcastAddr4(n *net.IPNet) net.IP {
	ip := n.IP.To4()
	broadcast := make(net.IP, len(ip))
	for i := 0; i < len(ip); i++ {
		broadcast[i] = ip[i] | ^n.Mask[i]
	}
	return broadcast
}

// hostsMax4 returns the maximum number of hosts in the given network.
func hostsMax4(n *net.IPNet) uint32 {
	ones, bits := n.Mask.Size()
	return 1<<uint32(bits-ones) - 2
}

func generateIPAddr(v4Net *net.IPNet, v6Net *net.IPNet, id uint32) (v4 net.IP, v6 net.IP, err error) {
	const idMin = 2

	if id < idMin {
		return nil, nil, errors.Wrapf(ErrInvalidID, "%d", id)
	}

	if v4Net == nil && v6Net == nil {
		return nil, nil, ErrInvalidCIDR
	}

	if v4Net != nil {
		v4 = cloneIP(v4Net.IP)
		for left, pos4 := id, len(v4)-1; left != 0; left, pos4 = left>>8, pos4-1 {
			decimalId := byte(left & 0xff)
			v4[pos4] += decimalId
		}

		if !v4Net.Contains(v4) || v4.Equal(net.IPv4(0xff, 0xff, 0xff, 0xff).Mask(v4Net.Mask)) || broadcastAddr4(v4Net).Equal(v4) {
			return nil, nil, ErrIPLimitExceeds
		}
	}

	if v6Net != nil {
		v6 = cloneIP(v6Net.IP)
		for left, pos6 := id, len(v6)-2; left != 0; left, pos6 = left>>8, pos6-2 {
			decimalId := byte(left & 0xff)
			hexId := uint16(decimalId%10) + uint16((decimalId/10)%10)*16 + uint16(decimalId/100)*256
			v6[pos6+0] += byte((hexId >> 8) & 0xff)
			v6[pos6+1] += byte(hexId & 0xff)
		}

		if !v6Net.Contains(v6) || !(v6.IsGlobalUnicast() || v6.IsLinkLocalUnicast()) {
			return nil, nil, ErrIPLimitExceeds
		}
	}

	return v4, v6, nil
}
