package presence

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/awilliams/wifi-presence/internal/hass"
	"github.com/awilliams/wifi-presence/internal/hostapd/hostapdtest"
	"github.com/awilliams/wifi-presence/internal/openwrt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func TestHostnameMQTTRotationAndRetainedInventory(t *testing.T) {
	if *mqttAddr == "" {
		t.Skip("skipping test; not given mqttAddr")
	}
	const oldMAC = "D0:E7:82:CE:1A:18"
	const newMAC = "AA:41:69:3F:E4:4F"
	handler := hostapdtest.DefaultHostAPDHandler(hostapdtest.StatusResp{
		State: "ENABLED", Channel: 42, SSID: "Home", BSSID: "aa:bb:cc:dd:ee:ff",
	}, []hostapdtest.StationResp{{MAC: oldMAC, Assoc: true}})
	events := make(chan string)
	handler.OnAttach(func() <-chan string { return events })
	source := &hintFixture{hints: map[string]openwrt.HostHint{
		strings.ToLower(oldMAC): {Name: "oren-phone"}, strings.ToLower(newMAC): {Name: "oren-phone"},
	}}
	daemon := newDaemonTestWithOptions(t, []Opt{WithHostSource(source, 20*time.Millisecond), WithDebounce(time.Second)}, handler)
	state := daemon.subTopic(daemon.topics.DeviceState("id-phone"), true)
	discovery := daemon.subTopic(daemon.topics.DeviceDiscovery("id-phone"), true)
	daemon.pubTopic(daemon.topics.Config(), true, hass.Configuration{Devices: []hass.TrackConfig{
		{ID: "phone", Name: "Phone", Hostname: "oren-phone"},
	}})
	receive := func(messages <-chan mqtt.Message) mqtt.Message {
		t.Helper()
		select {
		case err := <-daemon.errs:
			t.Fatal(err)
		case <-time.After(3 * time.Second):
			t.Fatal("MQTT message timeout")
		case msg := <-messages:
			return msg
		}
		return nil
	}
	if string(receive(state).Payload()) != hass.PayloadHome {
		t.Fatal("startup associated phone not home")
	}
	var device hass.DeviceTracker
	if err := json.Unmarshal(receive(discovery).Payload(), &device); err != nil {
		t.Fatal(err)
	}
	if device.UniqueID == "" || device.StateTopic != daemon.topics.DeviceState("id-phone") {
		t.Fatal("hostname discovery missing stable topic")
	}
	// A late subscriber must receive the inventory retained by the broker.
	inventory := daemon.subTopic(daemon.topics.Clients(), false)
	readInventory := func() hass.ClientInventory {
		t.Helper()
		var value hass.ClientInventory
		if err := json.Unmarshal(receive(inventory).Payload(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if got := readInventory(); len(got.Clients) != 1 || got.Clients[0].Hostname != "oren-phone" {
		t.Fatalf("retained inventory: %+v", got)
	}
	select {
	case events <- fmt.Sprintf("AP-STA-DISCONNECTED %s", oldMAC):
	case <-time.After(time.Second):
		t.Fatal("disconnect event timeout")
	}
	if got := readInventory(); len(got.Clients) != 0 {
		t.Fatalf("disconnected client stayed in inventory: %+v", got)
	}
	select {
	case events <- fmt.Sprintf("AP-STA-CONNECTED %s", newMAC):
	case <-time.After(time.Second):
		t.Fatal("connect event timeout")
	}
	if got := readInventory(); len(got.Clients) != 1 || got.Clients[0].MAC != newMAC {
		t.Fatalf("rotated MAC inventory: %+v", got)
	}
	attributes := daemon.subTopic(daemon.topics.DeviceJSONAttrs("id-phone"), false)
	var attrs hass.Attrs
	if err := json.Unmarshal(receive(attributes).Payload(), &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs.MAC != newMAC || attrs.MatchStatus != "matched" {
		t.Fatalf("identity did not follow MAC rotation: %+v", attrs)
	}
	select {
	case <-state:
		t.Fatal("MAC rotation emitted a departure despite debounce")
	case <-discovery:
		t.Fatal("MAC rotation recreated discovery identity")
	case <-time.After(50 * time.Millisecond):
	}
}
