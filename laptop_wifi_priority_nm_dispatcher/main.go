package main

import (
	"flag"
	"fmt"
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

// NetworkManager legacy IPv6 address:
// (ay, u, ay) == address, prefix, gateway
type nmIPv6Address struct {
	Address []byte
	Prefix  uint32
	Gateway []byte
}

// NetworkManager legacy IPv6 route:
// (ay, u, ay, u) == destination, prefix, next-hop, metric
type nmIPv6Route struct {
	Destination []byte
	Prefix      uint32
	NextHop     []byte
	Metric      uint32
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

/*
 * Convert legacy IPv6 address property back to its actual D-Bus type:
 *
 *     a(ayuay)
 *
 * godbus decodes incoming D-Bus structs as []interface{}.
 * Variant.Store() converts those generic structs into our typed struct.
 */
func normalizeIPv6Addresses(section map[string]dbus.Variant) error {
	v, ok := section["addresses"]
	if !ok {
		return nil
	}

	var addresses []nmIPv6Address

	if err := v.Store(&addresses); err != nil {
		return fmt.Errorf("decode ipv6.addresses: %w", err)
	}

	section["addresses"] = dbus.MakeVariant(addresses)

	return nil
}

/*
 * Convert legacy IPv6 route property back to:
 *
 *     a(ayuayu)
 */
func normalizeIPv6Routes(section map[string]dbus.Variant) error {
	v, ok := section["routes"]
	if !ok {
		return nil
	}

	var routes []nmIPv6Route

	if err := v.Store(&routes); err != nil {
		return fmt.Errorf("decode ipv6.routes: %w", err)
	}

	section["routes"] = dbus.MakeVariant(routes)

	return nil
}

/*
 * IPv4 addresses use:
 *
 *     aau
 *
 * which is [][]uint32 in Go.
 */
func normalizeIPv4Addresses(section map[string]dbus.Variant) error {
	v, ok := section["addresses"]
	if !ok {
		return nil
	}

	var addresses [][]uint32

	if err := v.Store(&addresses); err != nil {
		return fmt.Errorf("decode ipv4.addresses: %w", err)
	}

	section["addresses"] = dbus.MakeVariant(addresses)

	return nil
}

/*
 * IPv4 routes also use:
 *
 *     aau
 *
 * with four uint32 values per route:
 *
 *   destination
 *   prefix
 *   next-hop
 *   metric
 */
func normalizeIPv4Routes(section map[string]dbus.Variant) error {
	v, ok := section["routes"]
	if !ok {
		return nil
	}

	var routes [][]uint32

	if err := v.Store(&routes); err != nil {
		return fmt.Errorf("decode ipv4.routes: %w", err)
	}

	section["routes"] = dbus.MakeVariant(routes)

	return nil
}

/*
 * Normalize the legacy properties that godbus decodes as generic
 * interface slices.
 */
func normalizeSettings(
	settings map[string]map[string]dbus.Variant,
) error {
	if ipv6, ok := settings["ipv6"]; ok {
		if err := normalizeIPv6Addresses(ipv6); err != nil {
			return err
		}

		if err := normalizeIPv6Routes(ipv6); err != nil {
			return err
		}
	}

	if ipv4, ok := settings["ipv4"]; ok {
		if err := normalizeIPv4Addresses(ipv4); err != nil {
			return err
		}

		if err := normalizeIPv4Routes(ipv4); err != nil {
			return err
		}
	}

	return nil
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

	cfg, err := loadConfig(
		"/etc/laptop_wifi_priority_nm_pre_up.yml",
	)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	/*
	 * Connect directly to the system bus.
	 */
	bus, err := dbus.SystemBus()
	if err != nil {
		log.Fatalf("cannot connect to system D-Bus: %v", err)
	}

	settingsObj := bus.Object(
		nmService,
		dbus.ObjectPath(nmSettingsPath),
	)

	/*
	 * List saved connection object paths.
	 */
	var connectionPaths []dbus.ObjectPath

	err = settingsObj.Call(
		nmSettingsInterface+".ListConnections",
		0,
	).Store(&connectionPaths)

	if err != nil {
		log.Fatalf("failed to list NM connections: %v", err)
	}

	for _, connectionPath := range connectionPaths {
		connObj := bus.Object(
			nmService,
			connectionPath,
		)

		/*
		 * Get the complete connection profile.
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

		/*
		 * Identify the connection type.
		 */
		connectionType, ok := getString(
			settings,
			"connection",
			"type",
		)

		if !ok {
			log.Printf(
				" → skip %s: missing connection.type",
				connectionPath,
			)
			continue
		}

		if connectionType != "802-11-wireless" &&
			connectionType != "802-3-ethernet" {
			continue
		}

		/*
		 * Get connection ID.
		 */
		name, ok := getString(
			settings,
			"connection",
			"id",
		)

		if !ok {
			log.Printf(
				" → skip %s: missing connection.id",
				connectionPath,
			)
			continue
		}

		/*
		 * Restrict to -c when requested.
		 */
		if *connectionID != "" &&
			name != *connectionID {
			continue
		}

		log.Printf("Modifying connection: %s", name)

		/*
		 * IMPORTANT:
		 *
		 * GetSettings() gives us D-Bus Variants, but godbus represents
		 * incoming D-Bus structs generically.
		 *
		 * Restore the exact NetworkManager struct types before
		 * Update2().
		 */
		if err := normalizeSettings(settings); err != nil {
			log.Printf(
				" → skip %s: cannot normalize settings: %v",
				name,
				err,
			)
			continue
		}

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
		 * Public/default configuration.
		 */
		ipv6["dns-priority"] =
			dbus.MakeVariant(int32(1000))

		ipv6["dns-data"] =
			dbus.MakeVariant(cfg.PubIPv6)

		ipv4["dns-priority"] =
			dbus.MakeVariant(int32(201000))

		ipv4["dns-data"] =
			dbus.MakeVariant(cfg.PubIPv4)

		/*
		 * Private network.
		 */
		if hasPrefixAny(name, cfg.Prefixes) {
			log.Println(
				" -> Private network: applying private DNS + token",
			)

			ipv6["dns-data"] =
				dbus.MakeVariant(cfg.PrivIPv6)

			ipv6["token"] =
				dbus.MakeVariant(cfg.Ipv6Token)

			ipv4["dns-data"] =
				dbus.MakeVariant(cfg.PrivIPv4)

		} else if connectionType == "802-3-ethernet" {
			log.Println(
				" -> Ethernet network: restoring default DNS/token",
			)

			delete(ipv6, "token")
			delete(ipv6, "dns-data")
			delete(ipv4, "dns-data")

		} else {
			/*
			 * Non-private Wi-Fi.
			 */
			delete(ipv6, "token")
		}

		/*
		 * Persist the complete profile.
		 *
		 * 0x1 = NM_SETTINGS_UPDATE2_FLAG_TO_DISK
		 */
		var result map[string]dbus.Variant

		err = connObj.Call(
			nmConnectionInterface+".Update2",
			0,
			settings,
			uint32(1),
			map[string]dbus.Variant{},
		).Store(&result)

		if err != nil {
			log.Printf(
				" ⨉ failed to update %s: %v",
				name,
				err,
			)
			continue
		}

		log.Printf(" ◯ updated %s", name)
	}
}
