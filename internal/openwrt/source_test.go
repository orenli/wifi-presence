package openwrt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLeasesIgnoreExpiredAndUnknownNames(t *testing.T) {
	file := filepath.Join(t.TempDir(), "leases")
	data := "200 AA:BB:CC:DD:EE:FF 192.168.1.2 phone *\n" +
		"200 AA:BB:CC:DD:EE:FF 192.168.1.3 phone *\n" +
		"99 02:00:00:00:00:02 192.168.1.4 old *\n" +
		"0 02:00:00:00:00:03 192.168.1.5 * *\n" +
		"200 invalid 192.168.1.6 bad *\n200 02:00:00:00:00:04 invalid bad *\nmalformed\n"
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	hints, err := readLeases(file, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(hints) != 2 || len(hints["aa:bb:cc:dd:ee:ff"].IPv4) != 2 || hints["02:00:00:00:00:03"].Name != "" {
		t.Fatalf("unexpected hints: %+v", hints)
	}
}

func TestLocalUbusAndAssociationSnapshot(t *testing.T) {
	dir := t.TempDir()
	ubusScript := `#!/bin/sh
case "$3" in
getHostHints) printf '%s' '{"AA:BB:CC:DD:EE:FF":{"name":"inferred.lan","ipaddrs":["192.168.1.2"]}}';;
get_clients) printf '%s' '{"clients":{"AA:BB:CC:DD:EE:FF":{"assoc":true},"02:00:00:00:00:02":{"assoc":false}}}';;
*) exit 1;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ubus"), []byte(ubusScript), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	leaseFile := filepath.Join(dir, "leases")
	if err := os.WriteFile(leaseFile, []byte("0 aa:bb:cc:dd:ee:ff 192.168.1.2 advertised *\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := Source{LeaseFile: leaseFile}
	hints, err := source.Hints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hints["aa:bb:cc:dd:ee:ff"].Name != "advertised" || len(hints["aa:bb:cc:dd:ee:ff"].IPv4) != 1 {
		t.Fatalf("lease was not preferred or IP duplicated: %+v", hints)
	}
	clients, err := source.Clients(context.Background(), "phy0-ap0")
	if err != nil || len(clients) != 1 || clients[0] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("association snapshot: %v, %v", clients, err)
	}
	// Missing LuCI/ubus must fall back to dnsmasq leases without losing names.
	if err := os.WriteFile(filepath.Join(dir, "ubus"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hints, err = source.Hints(context.Background())
	if err != nil || hints["aa:bb:cc:dd:ee:ff"].Name != "advertised" {
		t.Fatalf("lease fallback: %+v, %v", hints, err)
	}
}
