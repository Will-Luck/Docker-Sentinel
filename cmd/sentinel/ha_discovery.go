package main

import (
	"encoding/json"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// applyHADiscovery connects ha to the first enabled MQTT channel when Home
// Assistant discovery is switched on, and disconnects it when it is off.
func applyHADiscovery(db *store.Store, ha *notify.HADiscovery, log *logging.Logger) {
	if enabled, _ := db.LoadSetting("ha_discovery_enabled"); enabled != "true" {
		if err := ha.Reconfigure(nil); err != nil {
			log.Warn("failed to stop HA discovery", "error", err)
		}
		return
	}
	channels, err := db.GetNotificationChannels()
	if err != nil {
		log.Warn("HA discovery: cannot read notification channels", "error", err)
		return
	}
	for _, ch := range channels {
		if ch.Type != notify.ProviderMQTT || !ch.Enabled {
			continue
		}
		var mqttSettings notify.MQTTSettings
		if json.Unmarshal(ch.Settings, &mqttSettings) != nil {
			continue
		}
		prefix, _ := db.LoadSetting("ha_discovery_prefix")
		clientID := mqttSettings.ClientID
		if clientID == "" {
			clientID = "docker-sentinel"
		}
		if err := ha.Reconfigure(&notify.HADiscoveryConfig{
			Broker:   mqttSettings.Broker,
			ClientID: clientID,
			Username: mqttSettings.Username,
			Password: mqttSettings.Password,
			Prefix:   prefix,
		}); err != nil {
			log.Warn("failed to start HA discovery", "error", err)
			return
		}
		log.Info("home assistant MQTT discovery enabled", "broker", mqttSettings.Broker)
		return
	}
	log.Warn("HA discovery is enabled but no enabled MQTT channel is configured")
}
