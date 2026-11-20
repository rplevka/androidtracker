package mqtt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/roman/kidstracker/internal/domain"
)

const (
	manufacturer = "Kidstracker"
	model        = "Android GPS tracker"
)

func normalizeTopicPart(value, fallback string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return fallback
	}
	if len(result) > 64 {
		return result[:64]
	}
	return result
}

func normalizePrefix(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		return "homeassistant"
	}
	parts := strings.Split(value, "/")
	for index, part := range parts {
		parts[index] = normalizeTopicPart(part, "homeassistant")
	}
	return strings.Join(parts, "/")
}

func normalizeBaseTopic(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "/")
	if value == "" {
		return "kidstracker"
	}
	parts := strings.Split(value, "/")
	for index, part := range parts {
		parts[index] = normalizeTopicPart(part, "kidstracker")
	}
	return strings.Join(parts, "/")
}

type Topics struct {
	Prefix        string
	Base          string
	Location      string
	Telemetry     string
	Config        string
	ConfigSet     string
	Zone          string
	Availability  string
	DiscoveryPath string
}

func TopicsFor(settings domain.MQTTSettings, deviceID string) Topics {
	prefix := normalizePrefix(settings.DiscoveryPrefix)
	base := normalizeBaseTopic(settings.BaseTopic) + "/" + normalizeTopicPart(deviceID, "device")
	return Topics{
		Prefix:        prefix,
		Base:          base,
		Location:      base + "/location",
		Telemetry:     base + "/telemetry",
		Config:        base + "/config",
		ConfigSet:     base + "/config/set",
		Zone:          base + "/zone",
		Availability:  normalizeBaseTopic(settings.BaseTopic) + "/status",
		DiscoveryPath: prefix,
	}
}

func discoveryMessages(settings domain.MQTTSettings, device domain.Device) map[string]string {
	topics := TopicsFor(settings, device.ID)
	deviceKey := "kidstracker_" + normalizeTopicPart(device.ID, "device")
	deviceInfo := map[string]any{
		"identifiers":  []string{deviceKey},
		"name":         device.Name,
		"manufacturer": manufacturer,
		"model":        model,
	}
	messages := make(map[string]string)
	add := func(component, objectID, topic string, payload map[string]any) {
		payload["unique_id"] = deviceKey + "_" + objectID
		payload["device"] = deviceInfo
		payload["availability_topic"] = topics.Availability
		payload["payload_available"] = "online"
		payload["payload_not_available"] = "offline"
		body, err := json.Marshal(payload)
		if err != nil {
			return
		}
		messages[fmt.Sprintf("%s/%s/%s/config", topics.DiscoveryPath, component, deviceKey+"_"+objectID)] = string(body)
	}
	value := func(template string) map[string]any {
		return map[string]any{"state_topic": topics.Location, "value_template": template}
	}
	entity := func(component, objectID, name, template string, extra map[string]any) {
		payload := value(template)
		payload["name"] = name
		for key, item := range extra {
			payload[key] = item
		}
		add(component, objectID, topics.Location, payload)
	}
	telemetryEntity := func(component, objectID, name, template string, extra map[string]any) {
		payload := map[string]any{"state_topic": topics.Telemetry, "value_template": template, "name": name}
		for key, item := range extra {
			payload[key] = item
		}
		add(component, objectID, topics.Telemetry, payload)
	}
	configSensor := func(objectID, name, template string, extra map[string]any) {
		payload := map[string]any{"state_topic": topics.Config, "value_template": template, "name": name}
		for key, item := range extra {
			payload[key] = item
		}
		add("sensor", objectID, topics.Config, payload)
	}
	configBinary := func(objectID, name, template string, extra map[string]any) {
		payload := map[string]any{"state_topic": topics.Config, "value_template": template, "name": name}
		for key, item := range extra {
			payload[key] = item
		}
		add("binary_sensor", objectID, topics.Config, payload)
	}
	configNumber := func(objectID, name, template, commandTemplate string, extra map[string]any) {
		payload := map[string]any{
			"state_topic":      topics.Config,
			"value_template":   template,
			"command_topic":    topics.ConfigSet,
			"command_template": commandTemplate,
			"name":             name,
			"entity_category":  "config",
		}
		for key, item := range extra {
			payload[key] = item
		}
		add("number", objectID, topics.Config, payload)
	}
	configSwitch := func(objectID, name string, extra map[string]any) {
		payload := map[string]any{
			"state_topic":     topics.Config,
			"value_template":  "{{ 'ON' if value_json.enabled else 'OFF' }}",
			"command_topic":   topics.ConfigSet,
			"payload_on":      `{"enabled":true}`,
			"payload_off":     `{"enabled":false}`,
			"state_on":        "ON",
			"state_off":       "OFF",
			"name":            name,
			"entity_category": "config",
		}
		for key, item := range extra {
			payload[key] = item
		}
		add("switch", objectID, topics.Config, payload)
	}

	add("device_tracker", "tracker", topics.Location, map[string]any{
		"name":                  device.Name,
		"json_attributes_topic": topics.Location,
		"source_type":           "gps",
	})

	if len(device.Zones) > 0 {
		add("sensor", "zone", topics.Zone, map[string]any{
			"name":                  "Zone",
			"state_topic":           topics.Zone,
			"value_template":        "{{ value_json.zone }}",
			"json_attributes_topic": topics.Zone,
			"icon":                  "mdi:map-marker-radius",
		})
		for _, zone := range device.Zones {
			objectID := "zone_" + normalizeTopicPart(zone.ID, "zone")
			add("binary_sensor", objectID, topics.Zone, map[string]any{
				"name":           "In " + zone.Name,
				"state_topic":    topics.Zone,
				"value_template": fmt.Sprintf("{{ 'ON' if '%s' in value_json.zone_ids else 'OFF' }}", zone.ID),
				"device_class":   "presence",
				"icon":           "mdi:map-marker-check",
			})
		}
	}

	entity("sensor", "battery", "Battery", "{{ value_json.battery_percent }}", map[string]any{
		"unit_of_measurement": "%", "device_class": "battery", "state_class": "measurement",
	})
	entity("binary_sensor", "charging", "Charging", "{{ 'ON' if value_json.charging else 'OFF' }}", map[string]any{
		"device_class": "battery_charging",
	})
	entity("sensor", "accuracy", "Location accuracy", "{{ value_json.gps_accuracy }}", map[string]any{
		"unit_of_measurement": "m", "device_class": "distance", "state_class": "measurement",
		"suggested_display_precision": 0,
	})
	entity("sensor", "speed", "Speed", "{{ (value_json.speed * 3.6) | round(1) if value_json.speed is defined else none }}", map[string]any{
		"unit_of_measurement": "km/h", "device_class": "speed", "state_class": "measurement",
	})
	entity("sensor", "altitude", "Altitude", "{{ value_json.altitude }}", map[string]any{
		"unit_of_measurement": "m", "device_class": "distance", "state_class": "measurement",
	})
	entity("sensor", "bearing", "Bearing", "{{ value_json.bearing }}", map[string]any{
		"unit_of_measurement": "°", "state_class": "measurement", "icon": "mdi:compass",
	})
	entity("sensor", "last_fix", "Last fix", "{{ value_json.recorded_at }}", map[string]any{
		"device_class": "timestamp",
	})
	entity("binary_sensor", "screen", "Screen", "{{ 'ON' if value_json.screen_on else 'OFF' }}", map[string]any{
		"icon": "mdi:cellphone", "entity_category": "diagnostic",
	})
	entity("binary_sensor", "locked", "Locked", "{{ 'ON' if value_json.device_locked else 'OFF' }}", map[string]any{
		"icon": "mdi:cellphone-lock", "entity_category": "diagnostic",
	})
	entity("binary_sensor", "doze", "Doze", "{{ 'ON' if value_json.device_idle else 'OFF' }}", map[string]any{
		"icon": "mdi:power-sleep", "entity_category": "diagnostic",
	})
	entity("binary_sensor", "power_save", "Power save", "{{ 'ON' if value_json.power_save_mode else 'OFF' }}", map[string]any{
		"icon": "mdi:battery-minus", "entity_category": "diagnostic",
	})
	entity("sensor", "network", "Network", "{{ value_json.network_type }}", map[string]any{
		"icon": "mdi:wifi", "entity_category": "diagnostic",
	})
	entity("sensor", "standby", "Standby bucket", "{{ value_json.standby_bucket }}", map[string]any{
		"icon": "mdi:timer-sand", "entity_category": "diagnostic",
	})
	entity("sensor", "gps_fix_duration", "Fix duration", "{{ value_json.gps_fix_duration_ms }}", map[string]any{
		"unit_of_measurement": "ms", "device_class": "duration", "state_class": "measurement",
		"entity_category": "diagnostic",
	})
	entity("sensor", "location_source", "Location source", "{{ value_json.location_source }}", map[string]any{
		"icon": "mdi:map-marker-radius", "entity_category": "diagnostic",
	})

	telemetryEntity("sensor", "battery_temperature", "Battery temperature", "{{ value_json.battery_temperature_c }}", map[string]any{
		"unit_of_measurement": "°C", "device_class": "temperature", "state_class": "measurement",
		"entity_category": "diagnostic",
	})
	telemetryEntity("sensor", "wake_delay", "Wake delay", "{{ value_json.wake_delay_seconds }}", map[string]any{
		"unit_of_measurement": "s", "device_class": "duration", "state_class": "measurement",
		"icon": "mdi:timer-alert", "entity_category": "diagnostic",
	})
	telemetryEntity("sensor", "sync_duration", "Sync duration", "{{ value_json.sync_duration_ms }}", map[string]any{
		"unit_of_measurement": "ms", "device_class": "duration", "state_class": "measurement",
		"icon": "mdi:timer", "entity_category": "diagnostic",
	})
	telemetryEntity("sensor", "queue_size", "Queued locations", "{{ value_json.queue_size }}", map[string]any{
		"icon": "mdi:database", "entity_category": "diagnostic",
	})
	telemetryEntity("binary_sensor", "gps_enabled", "GPS enabled", "{{ 'ON' if value_json.gps_enabled else 'OFF' }}", map[string]any{
		"icon": "mdi:crosshairs-gps", "entity_category": "diagnostic",
	})

	configBinary("tracking_enabled", "Tracking enabled", "{{ 'ON' if value_json.enabled else 'OFF' }}", map[string]any{
		"icon": "mdi:map-marker-path", "entity_category": "diagnostic",
	})
	configSensor("location_interval", "Location interval", "{{ value_json.location_interval_seconds }}", map[string]any{
		"unit_of_measurement": "s", "device_class": "duration", "state_class": "measurement",
		"entity_category": "diagnostic",
	})
	configSensor("sync_interval", "Sync interval", "{{ value_json.sync_interval_seconds }}", map[string]any{
		"unit_of_measurement": "s", "device_class": "duration", "state_class": "measurement",
		"entity_category": "diagnostic",
	})
	configSensor("disabled_poll_interval", "Disabled poll interval", "{{ value_json.sleep_duration_seconds }}", map[string]any{
		"unit_of_measurement": "s", "device_class": "duration", "state_class": "measurement",
		"entity_category": "diagnostic",
	})
	configSwitch("tracking_control", "Tracking control", map[string]any{
		"icon": "mdi:map-marker-path",
	})
	configNumber("location_interval_set", "Location interval (set)", "{{ value_json.location_interval_seconds }}",
		`{"location_interval_seconds": {{ value | int }}}`, map[string]any{
			"min": 5, "max": 86400, "step": 1, "mode": "box",
			"unit_of_measurement": "s", "device_class": "duration", "icon": "mdi:timer-outline",
		})
	configNumber("sync_interval_set", "Sync interval (set)", "{{ value_json.sync_interval_seconds }}",
		`{"sync_interval_seconds": {{ value | int }}}`, map[string]any{
			"min": 5, "max": 86400, "step": 1, "mode": "box",
			"unit_of_measurement": "s", "device_class": "duration", "icon": "mdi:timer-sync-outline",
		})
	configNumber("disabled_poll_interval_set", "Disabled poll interval (set)", "{{ value_json.sleep_duration_seconds }}",
		`{"sleep_duration_seconds": {{ value | int }}}`, map[string]any{
			"min": 10, "max": 604800, "step": 1, "mode": "box",
			"unit_of_measurement": "s", "device_class": "duration", "icon": "mdi:sleep",
		})
	configNumber("gps_timeout_set", "GPS timeout (set)", "{{ value_json.gps_timeout_seconds }}",
		`{"gps_timeout_seconds": {{ value | int }}}`, map[string]any{
			"min": 5, "max": 180, "step": 1, "mode": "box",
			"unit_of_measurement": "s", "device_class": "duration", "icon": "mdi:crosshairs-gps",
		})
	configNumber("min_distance_set", "Minimum distance (set)", "{{ value_json.min_distance_meters }}",
		`{"min_distance_meters": {{ value }}}`, map[string]any{
			"min": 0, "max": 10000, "step": 1, "mode": "box",
			"unit_of_measurement": "m", "device_class": "distance", "icon": "mdi:map-marker-distance",
		})
	return messages
}

func setOptional[T any](state map[string]any, key string, value *T) {
	if value != nil {
		state[key] = *value
	}
}

func locationState(location domain.Location) map[string]any {
	state := map[string]any{
		"latitude":     location.Latitude,
		"longitude":    location.Longitude,
		"gps_accuracy": location.AccuracyM,
		"recorded_at":  location.RecordedAt,
	}
	setOptional(state, "altitude", location.AltitudeM)
	setOptional(state, "speed", location.SpeedMPS)
	setOptional(state, "bearing", location.Bearing)
	setOptional(state, "battery_percent", location.BatteryPercent)
	setOptional(state, "charging", location.Charging)
	setOptional(state, "screen_on", location.ScreenOn)
	setOptional(state, "device_locked", location.DeviceLocked)
	setOptional(state, "device_idle", location.DeviceIdle)
	setOptional(state, "power_save_mode", location.PowerSaveMode)
	setOptional(state, "standby_bucket", location.StandbyBucket)
	setOptional(state, "network_type", location.NetworkType)
	setOptional(state, "gps_fix_duration_ms", location.GPSFixDurationMS)
	setOptional(state, "location_source", location.LocationSource)
	return state
}

func telemetryState(telemetry domain.Telemetry) map[string]any {
	state := map[string]any{"recorded_at": telemetry.RecordedAt}
	setOptional(state, "battery_percent", telemetry.BatteryPercent)
	setOptional(state, "charging", telemetry.Charging)
	setOptional(state, "screen_on", telemetry.ScreenOn)
	setOptional(state, "device_locked", telemetry.DeviceLocked)
	setOptional(state, "device_idle", telemetry.DeviceIdle)
	setOptional(state, "power_save_mode", telemetry.PowerSaveMode)
	setOptional(state, "standby_bucket", telemetry.StandbyBucket)
	setOptional(state, "network_type", telemetry.NetworkType)
	setOptional(state, "gps_enabled", telemetry.GPSEnabled)
	setOptional(state, "battery_temperature_c", telemetry.BatteryTemperatureC)
	setOptional(state, "wake_delay_seconds", telemetry.WakeDelaySeconds)
	setOptional(state, "sync_duration_ms", telemetry.SyncDurationMS)
	setOptional(state, "queue_size", telemetry.QueueSize)
	setOptional(state, "config_version", telemetry.ConfigVersion)
	return state
}

func configState(config domain.Config) map[string]any {
	return map[string]any{
		"enabled":                   config.Enabled,
		"location_interval_seconds": config.LocationIntervalSeconds,
		"sync_interval_seconds":     config.SyncIntervalSeconds,
		"sleep_duration_seconds":    config.SleepDurationSeconds,
		"gps_timeout_seconds":       config.GPSTimeoutSeconds,
		"min_distance_meters":       config.MinDistanceMeters,
		"version":                   config.Version,
	}
}

func marshalPayload(payload map[string]any) ([]byte, error) {
	return json.Marshal(payload)
}
