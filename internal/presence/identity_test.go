package presence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"github.com/awilliams/wifi-presence/internal/hass"
	"github.com/awilliams/wifi-presence/internal/hostapd"
	"github.com/awilliams/wifi-presence/internal/openwrt"
)

type identityRecorder struct {
	states       map[string]bool
	available    map[string]bool
	attrs        map[string]hass.Attrs
	registered   []hass.Discovery
	removed      []string
	inventory    hass.ClientInventory
	publications int
}

func (r *identityRecorder) RegisterDeviceTracker(_ context.Context, d hass.Discovery) error {
	r.registered = append(r.registered, d)
	return nil
}
func (r *identityRecorder) UnregisterDeviceTracker(_ context.Context, id string) error {
	r.removed = append(r.removed, id)
	return nil
}
func (r *identityRecorder) ClearStation(_ context.Context, id string) error {
	delete(r.states, id)
	delete(r.attrs, id)
	delete(r.available, id)
	return nil
}
func (r *identityRecorder) StationHome(_ context.Context, id string) error {
	r.states[id] = true
	r.publications++
	return nil
}
func (r *identityRecorder) StationNotHome(_ context.Context, id string) error {
	r.states[id] = false
	r.publications++
	return nil
}
func (r *identityRecorder) StationAttributes(_ context.Context, id string, a hass.Attrs) error {
	r.attrs[id] = a
	return nil
}
func (r *identityRecorder) DeviceAvailable(_ context.Context, id string, a bool) error {
	r.available[id] = a
	return nil
}
func (r *identityRecorder) Clients(_ context.Context, c hass.ClientInventory) error {
	r.inventory = c
	return nil
}

type hintFixture struct {
	hints map[string]openwrt.HostHint
	err   error
}

func (h *hintFixture) Hints(context.Context) (map[string]openwrt.HostHint, error) {
	return h.hints, h.err
}
func (h *hintFixture) Clients(context.Context, string) ([]string, error) { return nil, nil }

func identityFixture() (*identityTracker, *identityRecorder) {
	r := &identityRecorder{states: make(map[string]bool), available: make(map[string]bool), attrs: make(map[string]hass.Attrs)}
	return &identityTracker{publisher: r, apName: "home", logger: log.New(io.Discard, "", 0),
		debounce: 10 * time.Second, autodiscovery: true, complete: true,
		configured: make(map[string]*namedStation), observed: make(map[string]associatedClient), hints: make(map[string]openwrt.HostHint)}, r
}

func TestHostapdUppercaseMACJoinsLowercaseHostHints(t *testing.T) {
	tracker, recorder := identityFixture()
	tracker.hints["d0:e7:82:ce:1a:18"] = openwrt.HostHint{
		Name: "oren-phone", IPv4: []string{"192.168.1.42"}, IPv6: []string{"fd00::42"},
	}
	// Use the real association path, including the legacy MAC.String formatter,
	// instead of inserting an already-normalized observation by hand.
	ap := hap{client: &hostapd.Client{}, status: hostapd.Status{SSID: "Home"}}
	if err := tracker.addLocked(ap, "d0:e7:82:ce:1a:18"); err != nil {
		t.Fatal(err)
	}
	if err := tracker.configure(context.Background(), []hass.TrackConfig{{ID: "phone", Name: "Phone", Hostname: "oren-phone"}}); err != nil {
		t.Fatal(err)
	}
	if !recorder.states["id-phone"] {
		t.Fatal("uppercase observation failed to join lowercase resolver key")
	}
	client := recorder.inventory.Clients["Home"][0]
	if client.ID != "phone" || client.MAC != "D0:E7:82:CE:1A:18" || client.Hostname != "oren-phone" || len(client.IPv4) != 1 || len(client.IPv6) != 1 {
		t.Fatalf("hostname/IP enrichment failed: %+v", client)
	}
}

func TestGroupedInventorySchemaAndConfiguredIDs(t *testing.T) {
	tracker, recorder := identityFixture()
	tracker.ssids = map[string]bool{"Home 2G": true, "Home 5G": true}
	tracker.observed["first"] = associatedClient{
		Interface: "phy0-ap0", SSID: "Home 2G", BSSID: "aa:bb:cc:dd:ee:ff",
		MAC: "D0:E7:82:CE:1A:18", Hostname: "phone", Connected: true,
	}
	tracker.observed["other"] = associatedClient{SSID: "Home 2G", MAC: "AA:41:69:3F:E4:4F", Hostname: "guest", Connected: true}
	ctx := context.Background()
	if err := tracker.configure(ctx, []hass.TrackConfig{{ID: "phone-id", Name: "Phone", Hostname: "phone"}}); err != nil {
		t.Fatal(err)
	}
	if !recorder.inventory.StartupComplete || len(recorder.inventory.Clients) != 2 || recorder.inventory.Clients["Home 5G"] == nil {
		t.Fatal("missing startup status or empty SSID group")
	}
	hosts := recorder.inventory.Clients["Home 2G"]
	if len(hosts) != 2 || hosts[0].ID != "" || hosts[1].ID != "phone-id" {
		t.Fatalf("sort order or configured IDs incorrect: %+v", hosts)
	}
	payload, err := json.Marshal(recorder.inventory)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Fatalf("unexpected top-level fields: %s", payload)
	}
	groups := decoded["clients"].(map[string]interface{})
	for _, raw := range groups["Home 2G"].([]interface{}) {
		host := raw.(map[string]interface{})
		for _, field := range []string{"interface", "bssid", "ssid", "connected", "ap_name", "complete"} {
			if _, exists := host[field]; exists {
				t.Fatalf("redundant field %s in %s", field, payload)
			}
		}
		if host["ipv4"] == nil || host["ipv6"] == nil {
			t.Fatal("empty IP arrays serialized as null")
		}
	}
	tracker.observed["conflict"] = associatedClient{SSID: "Home 5G", MAC: "BB:41:69:3F:E4:4F", Hostname: "phone", Connected: true}
	if err := tracker.reconcileLocked(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, hosts := range recorder.inventory.Clients {
		for _, host := range hosts {
			if host.ID != "" {
				t.Fatal("ambiguous hostname inherited a configured ID")
			}
		}
	}
	delete(tracker.observed, "conflict")
	if err := tracker.configure(ctx, []hass.TrackConfig{
		{ID: "phone-id", Name: "Phone", Hostname: "phone"}, {ID: "second-id", Name: "Second", Hostname: "phone"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, host := range recorder.inventory.Clients["Home 2G"] {
		if host.ID != "" {
			t.Fatal("multiple matching trackers produced a nondeterministic ID")
		}
	}
	if err := tracker.configure(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if recorder.inventory.Clients["Home 2G"][1].ID != "" {
		t.Fatal("removed tracker left its ID in inventory")
	}
}

func TestHostnameDelayedLeaseRotationAndDeparture(t *testing.T) {
	ctx := context.Background()
	tracker, recorder := identityFixture()
	source := &hintFixture{hints: make(map[string]openwrt.HostHint)}
	tracker.source = source
	const oldMAC = "02:00:00:00:00:01"
	const newMAC = "02:00:00:00:00:02"
	key := observationKey("phy0-ap0", oldMAC)
	tracker.observed[key] = associatedClient{Interface: "phy0-ap0", MAC: oldMAC, Connected: true}
	if err := tracker.configure(ctx, []hass.TrackConfig{{ID: "phone", Name: "Phone", Hostname: "Oren-iPhone"}}); err != nil {
		t.Fatal(err)
	}
	if recorder.states["id-phone"] {
		t.Fatal("phone should not be matched before DHCP identifies it")
	}
	source.hints[oldMAC] = openwrt.HostHint{Name: "oren-iphone", IPv4: []string{"192.168.1.10"}}
	if err := tracker.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !recorder.states["id-phone"] || recorder.attrs["id-phone"].MAC != oldMAC {
		t.Fatal("late DHCP did not resolve the already-associated phone")
	}
	delete(tracker.observed, key)
	now := time.Now()
	if err := tracker.reconcileLocked(ctx, now); err != nil {
		t.Fatal(err)
	}
	if !recorder.states["id-phone"] {
		t.Fatal("departure bypassed debounce")
	}
	tracker.observed[observationKey("phy1-ap0", newMAC)] = associatedClient{Interface: "phy1-ap0", MAC: newMAC, Hostname: "oren-iphone", Connected: true}
	if err := tracker.reconcileLocked(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !recorder.states["id-phone"] || recorder.attrs["id-phone"].MAC != newMAC {
		t.Fatal("MAC rotation changed identity or lost presence")
	}
	if len(recorder.registered) != 1 || recorder.registered[0].ID != "phone" {
		t.Fatal("MAC rotation re-registered the device")
	}
	tracker.observed = make(map[string]associatedClient)
	if err := tracker.reconcileLocked(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := tracker.reconcileLocked(ctx, now.Add(13*time.Second)); err != nil {
		t.Fatal(err)
	}
	if recorder.states["id-phone"] {
		t.Fatal("phone stayed home past departure grace period")
	}
	// The lease remains present: refreshing it must not fabricate an association.
	if err := tracker.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.states["id-phone"] {
		t.Fatal("stale lease fabricated presence")
	}
	if err := tracker.configure(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, exists := recorder.states["id-phone"]; exists {
		t.Fatal("retained state was not cleared on removal")
	}
}

func TestHostnameAmbiguityAndDuplicateBSS(t *testing.T) {
	ctx := context.Background()
	tracker, recorder := identityFixture()
	tracker.observed["a"] = associatedClient{Interface: "a", MAC: "02:00:00:00:00:01", Hostname: "phone", Connected: true}
	tracker.observed["b"] = associatedClient{Interface: "b", MAC: "02:00:00:00:00:01", Hostname: "PHONE.", Connected: true}
	if err := tracker.configure(ctx, []hass.TrackConfig{{ID: "phone", Name: "Phone", Hostname: "phone"}}); err != nil {
		t.Fatal(err)
	}
	if !recorder.available["id-phone"] || !recorder.states["id-phone"] || len(recorder.inventory.Clients[""]) != 2 {
		t.Fatal("same MAC on two BSSs was treated as two phones or dropped from inventory")
	}
	tracker.observed["c"] = associatedClient{Interface: "c", MAC: "02:00:00:00:00:02", Hostname: "phone", Connected: true}
	if err := tracker.reconcileLocked(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if recorder.available["id-phone"] || recorder.attrs["id-phone"].MatchStatus != "ambiguous" || len(recorder.inventory.Clients[""]) != 3 {
		t.Fatal("duplicate hostname was silently merged")
	}
	delete(tracker.observed, "c")
	if err := tracker.reconcileLocked(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !recorder.available["id-phone"] {
		t.Fatal("ambiguity did not recover")
	}
}

func TestLookupFailurePreservesAssociation(t *testing.T) {
	tracker, recorder := identityFixture()
	tracker.source = &hintFixture{err: errors.New("ubus unavailable")}
	tracker.observed["a"] = associatedClient{MAC: "02:00:00:00:00:01", Hostname: "phone", Connected: true}
	ctx := context.Background()
	if err := tracker.configure(ctx, []hass.TrackConfig{{ID: "phone", Name: "Phone", Hostname: "phone"}}); err != nil {
		t.Fatal(err)
	}
	if err := tracker.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if !recorder.states["id-phone"] || recorder.inventory.LookupError == "" {
		t.Fatal("metadata failure fabricated a departure or was hidden")
	}
}

func TestSelectorValidation(t *testing.T) {
	for _, cfg := range []hass.TrackConfig{
		{Hostname: "phone", Name: "Phone"},
		{Hostname: "phone", Name: "Phone", ID: "bad/id"},
		{Hostname: "phone", Name: "Phone", ID: "phone", MAC: "02:00:00:00:00:01"},
		{Hostname: " ", Name: "Phone", ID: "phone"},
	} {
		if _, _, err := splitConfiguration(hass.Configuration{Devices: []hass.TrackConfig{cfg}}, true); err == nil {
			t.Fatalf("accepted invalid configuration %+v", cfg)
		}
	}
	legacy, named, err := splitConfiguration(hass.Configuration{Devices: []hass.TrackConfig{
		{Name: "old", MAC: "AA:BB:CC:DD:EE:FF"}, {ID: "Phone", Name: "Phone", Hostname: "phone"},
	}}, true)
	if err != nil || len(legacy.Devices) != 1 || len(named) != 1 || named[0].ID != "phone" {
		t.Fatalf("mixed config not supported: %v", err)
	}
	if _, _, err := splitConfiguration(hass.Configuration{Devices: []hass.TrackConfig{{ID: "phone", Name: "Phone", Hostname: "phone"}}}, false); err == nil {
		t.Fatal("hostname config accepted without resolver")
	}
}
