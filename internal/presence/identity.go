package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/awilliams/wifi-presence/internal/hass"
	"github.com/awilliams/wifi-presence/internal/hostapd"
	"github.com/awilliams/wifi-presence/internal/openwrt"
)

// HostSource enriches associations with hostnames. Clients is used only for
// the initial association snapshot, never to infer presence from a lease.
type HostSource interface {
	Hints(context.Context) (map[string]openwrt.HostHint, error)
	Clients(context.Context, string) ([]string, error)
}

func WithHostSource(source HostSource, interval time.Duration) Opt {
	return func(d *Daemon) { d.hostSource = source; d.hintInterval = interval }
}

type identityPublisher interface {
	RegisterDeviceTracker(context.Context, hass.Discovery) error
	UnregisterDeviceTracker(context.Context, string) error
	ClearStation(context.Context, string) error
	StationHome(context.Context, string) error
	StationNotHome(context.Context, string) error
	StationAttributes(context.Context, string, hass.Attrs) error
	DeviceAvailable(context.Context, string, bool) error
	Clients(context.Context, hass.ClientInventory) error
}

type namedStation struct {
	config                     hass.TrackConfig
	home, hasState             bool
	available, hasAvailability bool
	absentSince                time.Time
	attrs                      string
}

// associatedClient retains radio details for matching and roaming while the
// MQTT inventory publishes only host metadata grouped by SSID.
type associatedClient struct {
	Interface string
	Hostname  string
	MAC       string
	IPv4      []string
	IPv6      []string
	SSID      string
	BSSID     string
	Connected bool
}

// Identity state is separate from the legacy MAC trackers so existing topic
// names, discovery IDs, debounce behavior and MAC configuration keep working.
type identityTracker struct {
	mu                sync.Mutex
	publisher         identityPublisher
	source            HostSource
	logger            *log.Logger
	apName            string
	debounce          time.Duration
	autodiscovery     bool
	configured        map[string]*namedStation
	observed          map[string]associatedClient
	ssids             map[string]bool
	hints             map[string]openwrt.HostHint
	complete          bool
	snapshotRemaining int
	snapshotFailed    bool
	lookupError       string
	lastInventory     string
}

var deviceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func normalizedHostname(hostname string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
}

// splitConfiguration validates all selectors before either tracker is mutated.
func splitConfiguration(cfg hass.Configuration, hasSource bool) (hass.Configuration, []hass.TrackConfig, error) {
	var legacy hass.Configuration
	var named []hass.TrackConfig
	ids := make(map[string]bool)
	for _, device := range cfg.Devices {
		if device.Hostname == "" {
			var mac MAC
			if err := mac.Decode(device.MAC); err != nil {
				return legacy, nil, err
			}
			if device.ID != "" {
				return legacy, nil, fmt.Errorf("id is only supported with a hostname selector")
			}
			legacy.Devices = append(legacy.Devices, device)
			continue
		}
		if device.MAC != "" {
			return legacy, nil, fmt.Errorf("device %q must use exactly one of mac or hostname", device.Name)
		}
		if !hasSource {
			return legacy, nil, fmt.Errorf("hostname matching requires OpenWrt host lookup (-openwrt)")
		}
		if !deviceIDPattern.MatchString(device.ID) {
			return legacy, nil, fmt.Errorf("hostname device requires an id containing only letters, numbers, '_' or '-'")
		}
		if strings.TrimSpace(device.Name) == "" || normalizedHostname(device.Hostname) == "" {
			return legacy, nil, fmt.Errorf("hostname device requires a nonempty name and hostname")
		}
		device.ID = strings.ToLower(device.ID)
		if ids[device.ID] {
			return legacy, nil, fmt.Errorf("duplicate device id %q", device.ID)
		}
		ids[device.ID] = true
		named = append(named, device)
	}
	return legacy, named, nil
}

func (t *identityTracker) configure(ctx context.Context, devices []hass.TrackConfig) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := make(map[string]*namedStation, len(devices))
	for _, cfg := range devices {
		station := t.configured[cfg.ID]
		if station == nil {
			station = &namedStation{}
		}
		if station.config != cfg && t.autodiscovery {
			if err := t.publisher.RegisterDeviceTracker(ctx, hass.Discovery{Name: cfg.Name, ID: cfg.ID}); err != nil {
				return err
			}
		}
		station.config = cfg
		next[cfg.ID] = station
	}
	for id := range t.configured {
		if _, exists := next[id]; exists {
			continue
		}
		key := "id-" + id
		if t.autodiscovery {
			if err := t.publisher.UnregisterDeviceTracker(ctx, key); err != nil {
				return err
			}
		}
		if err := t.publisher.ClearStation(ctx, key); err != nil {
			return err
		}
	}
	t.configured = next
	return t.reconcileLocked(ctx, time.Now())
}

func observationKey(iface, mac string) string { return iface + "\x00" + mac }

func (t *identityTracker) addLocked(hap hap, value string) error {
	var mac MAC
	if err := mac.Decode(value); err != nil {
		return err
	}
	client := associatedClient{
		Interface: hap.client.InterfaceName(), MAC: mac.String(),
		SSID: hap.status.SSID, BSSID: hap.status.BSSID, Connected: true,
		IPv4: []string{}, IPv6: []string{},
	}
	t.enrichLocked(&client)
	t.observed[observationKey(client.Interface, client.MAC)] = client
	return nil
}

func (t *identityTracker) enrichLocked(client *associatedClient) {
	if hint, exists := t.hints[strings.ToLower(client.MAC)]; exists {
		if hint.Name != "" {
			client.Hostname = hint.Name
		}
		client.IPv4 = append([]string{}, hint.IPv4...)
		client.IPv6 = append([]string{}, hint.IPv6...)
	}
}

func (t *identityTracker) seed(ctx context.Context, haps []hap) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, hap := range haps {
		stations, err := hap.client.Stations()
		var macs []string
		if err == nil {
			for _, sta := range stations {
				if sta.Associated {
					macs = append(macs, sta.MAC)
				}
			}
		} else if t.source != nil {
			macs, err = t.source.Clients(ctx, hap.client.InterfaceName())
		}
		if err != nil {
			t.snapshotFailed = true
			t.logger.Printf("Initial inventory unavailable for %s: %v; waiting for association events", hap.client.InterfaceName(), err)
			continue
		}
		for _, mac := range macs {
			if err := t.addLocked(hap, mac); err != nil {
				t.snapshotFailed = true
				t.logger.Print(err)
			}
		}
	}
	t.snapshotRemaining -= len(haps)
	t.complete = t.snapshotRemaining <= 0 && !t.snapshotFailed
}

func (t *identityTracker) event(ctx context.Context, hap hap, event hostapd.Event) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch e := event.(type) {
	case hostapd.EventStationConnect:
		if err := t.addLocked(hap, e.MAC); err != nil {
			return err
		}
	case hostapd.EventStationDisconnect:
		var mac MAC
		if err := mac.Decode(e.MAC); err != nil {
			return err
		}
		delete(t.observed, observationKey(hap.client.InterfaceName(), mac.String()))
	default:
		return nil
	}
	return t.reconcileLocked(ctx, time.Now())
}

func (t *identityTracker) refresh(ctx context.Context) error {
	var hints map[string]openwrt.HostHint
	var err error
	if t.source != nil {
		hints, err = t.source.Hints(ctx)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		if t.lookupError != err.Error() {
			t.logger.Printf("Hostname lookup: %v", err)
		}
		t.lookupError = err.Error()
	} else {
		t.lookupError = ""
		if t.source != nil {
			t.hints = hints
		}
		for key, client := range t.observed {
			t.enrichLocked(&client)
			t.observed[key] = client
		}
	}
	return t.reconcileLocked(ctx, time.Now())
}

func (t *identityTracker) clientsLocked() []associatedClient {
	clients := make([]associatedClient, 0, len(t.observed))
	for _, client := range t.observed {
		clients = append(clients, client)
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Interface != clients[j].Interface {
			return clients[i].Interface < clients[j].Interface
		}
		return clients[i].MAC < clients[j].MAC
	})
	return clients
}

// matchHostname preserves observations on multiple BSSs, but distinguishes
// different MACs sharing a name. Ambiguous names must never bind silently.
func matchHostname(hostname string, clients []associatedClient) (*associatedClient, bool) {
	var selected *associatedClient
	for i := range clients {
		if normalizedHostname(clients[i].Hostname) != normalizedHostname(hostname) {
			continue
		}
		if selected != nil && selected.MAC != clients[i].MAC {
			return nil, true
		}
		if selected == nil {
			selected = &clients[i]
		}
	}
	return selected, false
}

func (t *identityTracker) reconcileLocked(ctx context.Context, now time.Time) error {
	clients := t.clientsLocked()
	for id, station := range t.configured {
		client, ambiguous := matchHostname(station.config.Hostname, clients)
		key := "id-" + id
		available := !ambiguous
		if !station.hasAvailability || station.available != available {
			if err := t.publisher.DeviceAvailable(ctx, key, available); err != nil {
				return err
			}
			station.available, station.hasAvailability = available, true
		}
		attrs := hass.Attrs{ID: station.config.ID, Name: station.config.Name, APName: t.apName, Hostname: station.config.Hostname, MatchStatus: "unmatched"}
		home := client != nil
		if ambiguous {
			attrs.MatchStatus = "ambiguous"
			station.absentSince = time.Time{}
		} else if home {
			station.absentSince = time.Time{}
			attrs.MAC, attrs.SSID, attrs.BSSID = client.MAC, client.SSID, client.BSSID
			attrs.Hostname, attrs.IPv4, attrs.IPv6 = client.Hostname, client.IPv4, client.IPv6
			attrs.IsConnected, attrs.MatchStatus = true, "matched"
		} else if station.home {
			if station.absentSince.IsZero() {
				station.absentSince = now
			}
			if now.Sub(station.absentSince) < t.debounce {
				home = true
			}
		}
		if !ambiguous && (!station.hasState || station.home != home) {
			var err error
			if home {
				err = t.publisher.StationHome(ctx, key)
			} else {
				err = t.publisher.StationNotHome(ctx, key)
			}
			if err != nil {
				return err
			}
			station.home, station.hasState = home, true
		}
		payload, err := json.Marshal(attrs)
		if err != nil {
			return err
		}
		if string(payload) != station.attrs {
			if err := t.publisher.StationAttributes(ctx, key, attrs); err != nil {
				return err
			}
			station.attrs = string(payload)
		}
	}
	inventory := t.inventoryLocked(clients)
	payload, err := json.Marshal(inventory)
	if err != nil {
		return err
	}
	if string(payload) != t.lastInventory {
		if err := t.publisher.Clients(ctx, inventory); err != nil {
			return err
		}
		t.lastInventory = string(payload)
	}
	return nil
}

func (t *identityTracker) inventoryLocked(clients []associatedClient) hass.ClientInventory {
	groups := make(map[string][]hass.InventoryClient, len(t.ssids))
	for ssid := range t.ssids {
		groups[ssid] = []hass.InventoryClient{}
	}
	ids := make(map[string][]string)
	for id, station := range t.configured {
		client, ambiguous := matchHostname(station.config.Hostname, clients)
		if client != nil && !ambiguous {
			mac := strings.ToLower(client.MAC)
			ids[mac] = append(ids[mac], id)
		}
	}
	for _, client := range clients {
		id := ""
		if matches := ids[strings.ToLower(client.MAC)]; len(matches) == 1 {
			id = matches[0]
		}
		groups[client.SSID] = append(groups[client.SSID], hass.InventoryClient{
			ID: id, Hostname: client.Hostname, MAC: client.MAC,
			IPv4: append([]string{}, client.IPv4...), IPv6: append([]string{}, client.IPv6...),
		})
	}
	for _, hosts := range groups {
		sort.SliceStable(hosts, func(i, j int) bool { return hosts[i].MAC < hosts[j].MAC })
	}
	return hass.ClientInventory{StartupComplete: t.complete, LookupError: t.lookupError, Clients: groups}
}
