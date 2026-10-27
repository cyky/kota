package check

import (
	"net/netip"
	"slices"
	"testing"
)

func TestVMAddrs(t *testing.T) {
	prefixes := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/8"),
		netip.MustParsePrefix("192.168.5.15/24"),
		netip.MustParsePrefix("192.168.64.2/24"),
		netip.MustParsePrefix("172.17.0.1/16"),
		netip.MustParsePrefix("169.254.10.5/16"),
		netip.MustParsePrefix("10.9.9.9/32"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fe80::5055:55ff:fe42:fad0/64"),
		netip.MustParsePrefix("fd21:7d39:506b:1de0:5055:55ff:fe67:4828/64"),
		netip.MustParsePrefix("2001:db8::5/128"),
	}
	want := []string{"192.168.5.15", "192.168.5.1", "192.168.64.2", "192.168.64.1", "172.17.0.1",
		"169.254.10.5", "169.254.0.1", "10.9.9.9",
		"fd21:7d39:506b:1de0:5055:55ff:fe67:4828", "fd21:7d39:506b:1de0::1", "2001:db8::5"}
	if got := vmAddrs(prefixes); !slices.Equal(got, want) {
		t.Errorf("vmAddrs = %v, want %v", got, want)
	}
}
