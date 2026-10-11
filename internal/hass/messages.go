package hass

import "time"

// Documentation:
// https://www.home-assistant.io/integrations/device_tracker.mqtt/

// Configuration describes the expected JSON configuration messages that
// are published to the config topic.
type Configuration struct {
	Devices []TrackConfig `json:"devices"`
}

// TrackConfig describes a single Wifi station/device to monitor for state changes.
type TrackConfig struct {
	Name     string `json:"name"`
	MAC      string `json:"mac,omitempty"`
	ID       string `json:"id,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

// DeviceTracker is used to configure HomeAssistant to track a device.
type DeviceTracker struct {
	Availability        []Availability `json:"availability,omitempty"`
	AvailabilityMode    string         `json:"availability_mode,omitempty"`
	AvailabilityTopic   string         `json:"availability_topic,omitempty"`    // The MQTT topic subscribed to receive availability (online/offline) updates.
	Device              Device         `json:"device,omitempty"`                // Information about the device this device tracker is a part of that ties it into the device registry. At least one of identifiers or connections must be present to identify the device.
	Icon                string         `json:"icon,omitempty"`                  // Icon for the entity. https://materialdesignicons.com
	JSONAttributesTopic string         `json:"json_attributes_topic,omitempty"` // The MQTT topic subscribed to receive a JSON dictionary payload and then set as device_tracker attributes. Usage example can be found in MQTT sensor documentation.
	Name                string         `json:"name,omitempty"`                  // The name of the MQTT device_tracker.
	ObjectID            string         `json:"object_id,omitempty"`             // Used instead of name for automatic generation of entity_id.
	PayloadAvailable    string         `json:"payload_available,omitempty"`     // Default: online. The payload that represents the available state.
	PayloadHome         string         `json:"payload_home,omitempty"`          // Default: home. The payload value that represents the ‘home’ state for the device.
	PayloadNotAvailable string         `json:"payload_not_available,omitempty"` // Default: offline. The payload that represents the unavailable state.
	PayloadNotHome      string         `json:"payload_not_home,omitempty"`      // Default: not_home. The payload value that represents the ‘not_home’ state for the device.
	QOS                 int            `json:"qos"`                             // The QoS level of the topic.
	SourceType          string         `json:"source_type,omitempty"`           // Attribute of a device tracker that affects state when being used to track a person. Valid options are gps, router, bluetooth, or bluetooth_le.
	StateTopic          string         `json:"state_topic"`                     // Required. The MQTT topic subscribed to receive device tracker state changes.
	UniqueID            string         `json:"unique_id,omitempty"`             // An ID that uniquely identifies this device_tracker. If two device_trackers have the same unique ID, Home Assistant will raise an exception.
}

// Device is part of the DeviceTracker configuration.
type Device struct {
	Identifiers  []string    `json:"identifiers,omitempty"`
	Connections  [][2]string `json:"connections,omitempty"`  // A list of connections of the device to the outside world as a list of tuples [connection_type, connection_identifier]. For example the MAC address of a network interface: 'connections': ['mac', '02:5b:26:a8:dc:12'].
	Name         string      `json:"name,omitempty"`         // The name of the device.
	ViaDevice    string      `json:"via_device,omitempty"`   // The name of the device.
	Manufacturer string      `json:"manufacturer,omitempty"` // The manufacturer of the device.
}

// Attrs are a device's attributes.
type Attrs struct {
	Hostname        string     `json:"hostname,omitempty"`
	IPv4            []string   `json:"ipv4,omitempty"`
	IPv6            []string   `json:"ipv6,omitempty"`
	MatchStatus     string     `json:"match_status,omitempty"`
	Name            string     `json:"name"`
	MAC             string     `json:"mac_address"`
	IsConnected     bool       `json:"is_connected"`
	APName          string     `json:"ap_name"`
	SSID            string     `json:"ssid"`
	BSSID           string     `json:"bssid"`
	ConnectedAt     *time.Time `json:"connected_at,omitempty"`
	ConnectedFor    int        `json:"connected_for,omitempty"`
	DisconnectedAt  *time.Time `json:"disconnected_at,omitempty"`
	DisconnectedFor int        `json:"disconnected_for,omitempty"`
}

// Availability describes an additional MQTT availability source.
type Availability struct {
	Topic string `json:"topic"`
}

// ClientInventory includes every observed association, including untracked clients.
type ClientInventory struct {
	StartupComplete bool                         `json:"startup_complete"`
	LookupError     string                       `json:"hostname_lookup_error,omitempty"`
	Clients         map[string][]InventoryClient `json:"clients"`
}

type InventoryClient struct {
	ID       string   `json:"id,omitempty"`
	Hostname string   `json:"hostname"`
	MAC      string   `json:"mac"`
	IPv4     []string `json:"ipv4"`
	IPv6     []string `json:"ipv6"`
}
