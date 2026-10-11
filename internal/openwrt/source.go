// Package openwrt resolves local host hints without router administration credentials.
package openwrt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type HostHint struct {
	Name string   `json:"name"`
	IPv4 []string `json:"ipaddrs"`
	IPv6 []string `json:"ip6addrs"`
}

type Source struct{ LeaseFile string }

func canonicalMAC(value string) (string, error) {
	mac, err := net.ParseMAC(value)
	if err != nil || len(mac) != 6 {
		return "", fmt.Errorf("invalid station MAC %q", value)
	}
	return mac.String(), nil
}

func ubus(ctx context.Context, object, method string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ubus", "call", object, method).Output()
	if err != nil {
		return nil, fmt.Errorf("ubus %s %s: %w", object, method, err)
	}
	return output, nil
}

// Hints uses local ubus host hints and active dnsmasq leases. A live lease's
// advertised hostname takes precedence over an inferred DNS hostname.
func (s Source) Hints(ctx context.Context) (map[string]HostHint, error) {
	hints := make(map[string]HostHint)
	output, lookupErr := ubus(ctx, "luci-rpc", "getHostHints")
	if lookupErr == nil {
		var raw map[string]HostHint
		if lookupErr = json.Unmarshal(output, &raw); lookupErr == nil {
			for key, hint := range raw {
				mac, err := canonicalMAC(key)
				if err == nil {
					hints[mac] = hint
				}
			}
		}
	}
	leases, leaseErr := readLeases(s.LeaseFile, time.Now())
	if leaseErr != nil && lookupErr != nil {
		return nil, fmt.Errorf("hostname lookup failed: %v; leases: %w", lookupErr, leaseErr)
	}
	for mac, lease := range leases {
		hint := hints[mac]
		if lease.Name != "" {
			hint.Name = lease.Name
		}
		hint.IPv4 = appendUnique(hint.IPv4, lease.IPv4...)
		hints[mac] = hint
	}
	return hints, nil
}

func appendUnique(values []string, added ...string) []string {
	for _, value := range added {
		found := false
		for _, old := range values {
			if old == value {
				found = true
				break
			}
		}
		if !found {
			values = append(values, value)
		}
	}
	return values
}

func readLeases(filename string, now time.Time) (map[string]HostHint, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hints := make(map[string]HostHint)
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) < 4 {
			continue
		}
		expires, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || (expires != 0 && expires <= now.Unix()) {
			continue
		}
		mac, err := canonicalMAC(fields[1])
		if err != nil {
			continue
		}
		ip := net.ParseIP(fields[2])
		if ip == nil || ip.To4() == nil {
			continue
		}
		hint := hints[mac]
		if fields[3] != "*" {
			hint.Name = fields[3]
		}
		hint.IPv4 = appendUnique(hint.IPv4, ip.String())
		hints[mac] = hint
	}
	return hints, scan.Err()
}

// Clients supplies an association-only startup snapshot for stripped-down
// hostapd builds which cannot handle STA-FIRST/STA-NEXT. DHCP is never used
// as evidence that a device is connected.
func (s Source) Clients(ctx context.Context, iface string) ([]string, error) {
	output, err := ubus(ctx, "hostapd."+iface, "get_clients")
	if err != nil {
		return nil, err
	}
	var response struct {
		Clients map[string]struct {
			Associated bool `json:"assoc"`
		} `json:"clients"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, err
	}
	if response.Clients == nil {
		return nil, fmt.Errorf("hostapd.%s returned no clients field", iface)
	}
	clients := make([]string, 0, len(response.Clients))
	for key, station := range response.Clients {
		if !station.Associated {
			continue
		}
		mac, err := canonicalMAC(key)
		if err != nil {
			return nil, err
		}
		clients = append(clients, mac)
	}
	return clients, nil
}
