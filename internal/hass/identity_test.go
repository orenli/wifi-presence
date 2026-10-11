package hass

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type recordedPublication struct {
	topic    string
	retained bool
	payload  interface{}
}
type publicationClient struct {
	mqtt.Client
	messages []recordedPublication
}
type publicationToken struct{ done chan struct{} }

func (t publicationToken) Wait() bool                     { return true }
func (t publicationToken) WaitTimeout(time.Duration) bool { return true }
func (t publicationToken) Done() <-chan struct{}          { return t.done }
func (t publicationToken) Error() error                   { return nil }
func (c *publicationClient) Publish(topic string, _ byte, retained bool, payload interface{}) mqtt.Token {
	c.messages = append(c.messages, recordedPublication{topic, retained, payload})
	done := make(chan struct{})
	close(done)
	return publicationToken{done}
}

func TestStableHostnameDiscoveryAndLegacyIdentity(t *testing.T) {
	client := &publicationClient{}
	m := &MQTT{c: client, apName: "AP", topics: &MQTTTopics{Name: "AP", Prefix: "wifi-presence", HASSPrefix: "homeassistant"}}
	ctx := context.Background()
	if err := m.RegisterDeviceTracker(ctx, Discovery{ID: "phone", Name: "Phone"}); err != nil {
		t.Fatal(err)
	}
	msg := client.messages[0]
	if msg.topic != "homeassistant/device_tracker/ap/id-phone/config" || !msg.retained {
		t.Fatalf("unexpected publication: %+v", msg)
	}
	var tracker DeviceTracker
	if err := json.Unmarshal(msg.payload.([]byte), &tracker); err != nil {
		t.Fatal(err)
	}
	if tracker.UniqueID != "wifipresence_id-phone_ap" || tracker.StateTopic != "wifi-presence/station/ap/id-phone/state" || len(tracker.Device.Identifiers) != 1 || len(tracker.Availability) != 2 || tracker.AvailabilityMode != "all" {
		t.Fatalf("unstable discovery or wrong availability: %+v", tracker)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(msg.payload.([]byte), &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["device"].(map[string]interface{})["connections"]; exists {
		t.Fatal("hostname device should not have a transient MAC connection")
	}
	if err := m.RegisterDeviceTracker(ctx, Discovery{MAC: "AA:BB:CC:DD:EE:FF", Name: "Old"}); err != nil {
		t.Fatal(err)
	}
	msg = client.messages[1]
	if err := json.Unmarshal(msg.payload.([]byte), &tracker); err != nil {
		t.Fatal(err)
	}
	if msg.topic != "homeassistant/device_tracker/ap/aa-bb-cc-dd-ee-ff/config" || tracker.UniqueID != "wifipresence_aabbccddeeff_ap" || tracker.AvailabilityTopic != "wifi-presence/ap/status" {
		t.Fatalf("legacy identity changed: %+v", tracker)
	}
	if err := m.Clients(ctx, ClientInventory{StartupComplete: true, Clients: map[string][]InventoryClient{"Home": {}}}); err != nil {
		t.Fatal(err)
	}
	if client.messages[2].topic != "wifi-presence/ap/clients" || !client.messages[2].retained {
		t.Fatal("inventory was not retained per AP")
	}
	if err := m.ClearStation(ctx, "id-phone"); err != nil {
		t.Fatal(err)
	}
	for _, msg := range client.messages[3:] {
		if !msg.retained || len(msg.payload.([]byte)) != 0 {
			t.Fatal("removed device left retained data")
		}
	}
}
