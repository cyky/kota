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
	}
	want := []string{"192.168.5.15", "192.168.5.1", "192.168.64.2", "192.168.64.1", "172.17.0.1"}
	if got := vmAddrs(prefixes); !slices.Equal(got, want) {
		t.Errorf("vmAddrs = %v, want %v", got, want)
	}
}
