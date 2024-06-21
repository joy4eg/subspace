package main

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateIPAddr(t *testing.T) {
	_, v4Net, err := net.ParseCIDR("127.10.0.0/16")
	require.NoError(t, err)

	_, v6Net, err := net.ParseCIDR("fe80::/112")
	require.NoError(t, err)
	{
		ipv4, ipv6, err := generateIPAddr(v4Net, v6Net, 100)
		require.NoError(t, err)
		if expected := "127.10.0.100"; ipv4.String() != expected {
			t.Errorf("Failed to generate IPv4: %s(expected) != %s(actual)", ipv4.String(), expected)
		}
		if expected := "fe80::100"; ipv6.String() != expected {
			t.Errorf("Failed to generate IPv6: %s(expected) != %s(actual)", ipv6.String(), expected)
		}
	}
	_, ipv6, err := generateIPAddr(v4Net, v6Net, 256)
	if err == nil {
		t.Errorf("%s contain only 255 valid v6 address, but got: %s", v6Net.String(), ipv6.String())
	}

	_, v4Net, _ = net.ParseCIDR("127.10.10.128/25")
	_, v6Net, _ = net.ParseCIDR("fe80::/64")
	ipv4, _, err := generateIPAddr(v4Net, v6Net, 129)
	if err == nil {
		t.Errorf("%s contain only 126 valid v4 address, but got: %s", v4Net.String(), ipv4.String())
	}

	t.Run("InvalidID", func(t *testing.T) {
		_, _, err := generateIPAddr(v4Net, v6Net, 1)
		require.ErrorIs(t, err, ErrInvalidID)
	})
}

func testCalcDefaultGateway(t *testing.T, validCIDR, expectedGateway string, invalidCIDR string) {
	{
		ip, network, err := calcDefaultGateway(validCIDR)
		require.NoError(t, err)
		if !ip.Equal(net.ParseIP(expectedGateway)) {
			t.Errorf("Default gateway of %s must be %s, but got %s (in %s)", validCIDR, expectedGateway, ip.String(), network.String())
		}
	}
	{
		if ip, network, err := calcDefaultGateway(invalidCIDR); err == nil {
			t.Errorf("There should not be default GW for %s, but got %s(in %s)", invalidCIDR, ip.String(), network.String())
		}
	}
}

func TestCalcDefaultGatewayV6(t *testing.T) {
	testCalcDefaultGateway(t, "fe80:1234:1234:1234::/64", "fe80:1234:1234:1234::1", "fe80:1234:1234:1234::/128")
}
func TestCalcDefaultGatewayV4(t *testing.T) {
	testCalcDefaultGateway(t, "127.168.128.0/18", "127.168.128.1", "127.168.128.0/32")
}

func TestGenerateIPAddrV4Only(t *testing.T) {
	t.Run("CIDR_24", func(t *testing.T) {
		testnet := &net.IPNet{
			IP:   net.IPv4(127, 0, 0, 0),
			Mask: net.IPv4Mask(255, 255, 255, 0),
		}

		m := hostsMax4(testnet)
		require.Equal(t, uint32(254), m)

		for i := range m - 1 {
			_, _, err := generateIPAddr(testnet, nil, uint32(i)+2)
			require.NoError(t, err)
		}

		_, _, err := generateIPAddr(testnet, nil, 255)
		require.ErrorIs(t, err, ErrIPLimitExceeds)
	})
	t.Run("CIDR_20", func(t *testing.T) {
		testnet := &net.IPNet{
			IP:   net.IPv4(127, 0, 0, 0),
			Mask: net.IPv4Mask(255, 255, 240, 0),
		}

		m := hostsMax4(testnet)
		require.Equal(t, uint32(4094), m)

		for i := range hostsMax4(testnet) - 1 {
			_, _, err := generateIPAddr(testnet, nil, uint32(i)+2)
			require.NoError(t, err)
		}

		_, _, err := generateIPAddr(testnet, nil, 4095)
		require.ErrorIs(t, err, ErrIPLimitExceeds)
	})
}
