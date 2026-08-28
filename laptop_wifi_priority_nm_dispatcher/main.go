package main

import (
	"flag"
	"log"
	"os"

	"github.com/godbus/dbus/v5"
	"gopkg.in/yaml.v2"
)

const (
	nmService             = "org.freedesktop.NetworkManager"
	nmSettingsPath        = "/org/freedesktop/NetworkManager/Settings"
	nmSettingsInterface   = "org.freedesktop.NetworkManager.Settings"
	nmConnectionInterface = "org.freedesktop.NetworkManager.Settings.Connection"
)

type Config struct {
	Prefixes  []string `yaml:"prefixes"`
	PrivIPv6  []string `yaml:"priv_ipv6"`
	PrivIPv4  []string `yaml:"priv_ipv4"`
	PubIPv6   []string `yaml:"pub_ipv6"`
	PubIPv4   []string `yaml:"pub_ipv4"`
	Ipv6Token string   `yaml:"ipv6_token"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func hasPrefixAny(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if len(name) >= len(p) && name[:len(p)] == p {
			return true
		}
	}

	return false
}

func getString(
	settings map[string]map[string]dbus.Variant,
	section string,
	property string,
) (string, bool) {
	sectionMap, ok := settings[section]
	if !ok {
		return "", false
	}

	v, ok := sectionMap[property]
	if !ok {
		return "", false
	}

	s, ok := v.Value().(string)
	return s, ok
}

func main() {
	currentIf := flag.String("i", "", "")
	_ = flag.String("a", "", "")
	connectionID := flag.String("c", "", "")

	flag.Parse()

	log.Printf(
		"started for IF: %s; CON ID: %s",
		*currentIf,
		*connectionID,
	)

	cfg, err := loadConfig("/etc/laptop_wifi_priority_nm_pre_up.yml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Connect to the system D-Bus.
	bus, err := dbus.SystemBus()
	if err != nil {
		log.Fatalf("cannot connect to system D-Bus: %v", err)
	}

	// Get NetworkManager Settings object.
	settingsObj := bus.Object(
		nmService,
		dbus.ObjectPath(nmSettingsPath),
	)

	// List saved connection object paths.
	var connectionPaths []dbus.ObjectPath

	err = settingsObj.Call(
		nmSettingsInterface+".ListConnections",
		0,
	).Store(&connectionPaths)
	if err != nil {
		log.Fatalf("failed to list NM connections: %v", err)
	}

	for _, connectionPath := range connectionPaths {
		connObj := bus.Object(nmService, connectionPath)

		/*
		 * GetSettings directly through D-Bus.
		 *
		 * This is the important difference from gonetworkmanager:
		 * values remain dbus.Variant values, preserving their original
		 * D-Bus signatures.
		 */
		var settings map[string]map[string]dbus.Variant

		err := connObj.Call(
			nmConnectionInterface+".GetSettings",
			0,
		).Store(&settings)

		if err != nil {
			log.Printf(
				" → skip %s: cannot read settings: %v",
				connectionPath,
				err,
			)
			continue
		}

		connectionSection, ok := settings["connection"]
		if !ok {
			log.Printf(
				" → skip %s: missing connection section",
				connectionPath,
			)
			continue
		}

		/*
		 * connection.type
		 */
		typeVariant, ok := connectionSection["type"]
		if !ok {
			log.Printf(
				" → skip %s: missing connection.type",
				connectionPath,
			)
			continue
		}

		connectionType, ok := typeVariant.Value().(string)
		if !ok {
			log.Printf(
				" → skip %s: invalid connection.type",
				connectionPath,
			)
			continue
		}

		if connectionType != "802-11-wireless" &&
			connectionType != "802-3-ethernet" {
			continue
		}

		/*
		 * connection.id
		 */
		name, ok := getString(settings, "connection", "id")
		if !ok {
			log.Printf(
				" → skip %s: missing connection.id",
				connectionPath,
			)
			continue
		}

		/*
		 * Restrict to one connection when -c was supplied.
		 */
		if *connectionID != "" && name != *connectionID {
			continue
		}

		log.Printf("Modifying connection: %s", name)

		ipv6, ok := settings["ipv6"]
		if !ok {
			log.Printf(
				" → skip %s: missing ipv6 settings",
				name,
			)
			continue
		}

		ipv4, ok := settings["ipv4"]
		if !ok {
			log.Printf(
				" → skip %s: missing ipv4 settings",
				name,
			)
			continue
		}

		/*
		 * Defaults.
		 *
		 * Everything else in ipv4/ipv6 remains untouched.
		 */
		ipv6["dns-priority"] = dbus.MakeVariant(int32(1000))
		ipv6["dns-data"] = dbus.MakeVariant(cfg.PubIPv6)

		ipv4["dns-priority"] = dbus.MakeVariant(int32(201000))
		ipv4["dns-data"] = dbus.MakeVariant(cfg.PubIPv4)

		/*
		 * Private network.
		 */
		if hasPrefixAny(name, cfg.Prefixes) {
			log.Println(" -> Private network: applying private DNS + token")

			ipv6["dns-data"] = dbus.MakeVariant(cfg.PrivIPv6)
			ipv6["token"] = dbus.MakeVariant(cfg.Ipv6Token)

			ipv4["dns-data"] = dbus.MakeVariant(cfg.PrivIPv4)

		} else if connectionType == "802-3-ethernet" {
			log.Println(" -> Ethernet network: restoring default DNS/token")

			/*
			 * Removing the Variant means the setting/property is
			 * omitted from the resulting profile.
			 */
			delete(ipv6, "token")
			delete(ipv6, "dns-data")
			delete(ipv4, "dns-data")
		} else {
			/*
			 * Non-private Wi-Fi: use public DNS and no token.
			 */
			delete(ipv6, "token")
		}

		/*
		 * Update2() still requires the complete connection settings.
		 *
		 * However, because we got the settings directly as
		 * map[string]map[string]dbus.Variant, properties we did not
		 * touch retain their original D-Bus signatures.
		 */
		var result map[string]dbus.Variant

		err = connObj.Call(
			nmConnectionInterface+".Update2",
			0,
			settings,
			uint32(1), // to-disk
			map[string]dbus.Variant{},
		).Store(&result)

		if err != nil {
			log.Printf(
				" ✗ failed to update %s: %v",
				name,
				err,
			)
			continue
		}

		log.Printf(" ✓ updated %s", name)
	}
}
