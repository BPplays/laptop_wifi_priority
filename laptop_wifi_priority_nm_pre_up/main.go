package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Wifx/gonetworkmanager"
	"gopkg.in/yaml.v2"
)

// Config holds the list of SSID prefixes and DNS/token settings
type Config struct {
	Prefixes []string `yaml:"prefixes"`
	PrivIPv6 []string `yaml:"priv_ipv6"`
	PrivIPv4 []string `yaml:"priv_ipv4"`
	PubIPv6  []string `yaml:"pub_ipv6"`
	PubIPv4  []string `yaml:"pub_ipv4"`
	Ipv6Token    string   `yaml:"ipv6_token"`
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

func main() {
	// Load your YAML config
	cfg, err := loadConfig("/etc/laptop_wifi_priority_nm_pre_up.yml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Connect to NM Settings interface
	settingsSvc, err := gonetworkmanager.NewSettings()
	if err != nil {
		log.Fatalf("cannot connect to NM Settings: %v", err)
	}

	// List all saved connections
	conns, err := settingsSvc.ListConnections()
	if err != nil {
		log.Fatalf("failed to list NM connections: %v", err)
	}

	// Iterate & patch each Wi‑Fi connection
	for _, conn := range conns {
		// Fetch full settings map
		sMap, err := conn.GetSettings()
		if err != nil {
			log.Printf(" → skip %s: cannot read settings: %v", conn.GetPath(), err)
			continue
		}

		// Only care about 802‑11‑wireless
		cType := sMap["connection"]["type"].(string)
		if cType != "802-11-wireless" && cType != "802-3-ethernet" {
			continue
		}

		name := sMap["connection"]["id"].(string)
		fmt.Printf("Modifying connection: %s\n", name)

		ipv6 := map[string]any{
			"method":         "auto",
			"addr-gen-mode":  "eui64",
			"ip6-privacy":    "prefer-temp-addr",
			"dns-priority":   int32(1),
			"dns":            cfg.PubIPv6,
			"token":          nil,
		}

		ipv4 := map[string]any{
			// "method":       "auto",
			"dns-priority": int32(2),
			"dns":          cfg.PubIPv4,
		}

		fmt.Println(cfg.PrivIPv6)
		fmt.Println(cfg.PrivIPv4)
		fmt.Println(cfg.PubIPv6)
		fmt.Println(cfg.PubIPv4)

		if hasPrefixAny(name, cfg.Prefixes) {
			fmt.Println(" -> Private network: applying private DNS + token")
			ipv6["dns"] = cfg.PrivIPv6
			ipv6["token"] = cfg.Ipv6Token
			ipv4["dns"] = cfg.PrivIPv4
		} else if cType == "802-3-ethernet" {

			ipv6["dns"] = cfg.PrivIPv6
			ipv4["dns"] = cfg.PrivIPv4

			ipv6["token"] = nil
			// ipv6["dns"] = nil
			// ipv4["dns"] = nil
		}

		// Inject our maps back into the connection settings
		sMap["ipv6"] = ipv6
		sMap["ipv4"] = ipv4

		// Commit the update
		if err := conn.Update(sMap); err != nil {
			log.Printf(" ✗ failed to update %s: %v", name, err)
		} else {
			fmt.Printf(" ✓ updated %s\n", name)
		}
	}
}
