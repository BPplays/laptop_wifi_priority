package main

import (
	"fmt"
	"log"
	"os"
	"net"
    "encoding/binary"

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

func ipsToBytes(addrs []string) ([][]byte, error) {
    var out [][]byte
    for _, s := range addrs {
        ip := net.ParseIP(s).To16()
        if ip == nil {
            return nil, fmt.Errorf("invalid IPv6 address: %s", s)
        }
        out = append(out, ip)
    }
    return out, nil
}

func ipsToUint32(addrs []string) ([]uint32, error) {
    var out []uint32
    for _, s := range addrs {
        ip := net.ParseIP(s).To4()
        if ip == nil {
            return nil, fmt.Errorf("invalid IPv4 address: %s", s)
        }
        out = append(out, binary.BigEndian.Uint32(ip))
    }
    return out, nil
}

func main() {
	// Load your YAML config
	cfg, err := loadConfig("/etc/laptop_wifi_priority_nm_pre_up.yml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// fmt.Println("token to nmformat: %v")

    privIPv6Bytes, err := ipsToBytes(cfg.PrivIPv6)
    if err != nil {
        log.Fatalf("invalid priv_ipv6 in config: %v", err)
    }
    pubIPv6Bytes, err := ipsToBytes(cfg.PubIPv6)
    if err != nil {
        log.Fatalf("invalid pub_ipv6 in config: %v", err)
    }

    privIPv4Nums, err := ipsToUint32(cfg.PrivIPv4)
    if err != nil {
        log.Fatalf("invalid priv_ipv4 in config: %v", err)
    }
    pubIPv4Nums, err := ipsToUint32(cfg.PubIPv4)
    if err != nil {
        log.Fatalf("invalid pub_ipv4 in config: %v", err)
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
			"addr-gen-mode":  int32(0), // use eui-64
			"ip6-privacy":    int32(2),
			"dns-priority":   int32(1),
			"dns":            pubIPv6Bytes,
			"token":          nil,
		}

		ipv4 := map[string]any{
			"method":       "auto",
			"dns-priority": int32(2),
			"dns":          pubIPv4Nums,
		}

		fmt.Println(cfg.PrivIPv6)
		fmt.Println(cfg.PrivIPv4)
		fmt.Println(cfg.PubIPv6)
		fmt.Println(cfg.PubIPv4)


		fmt.Println(cfg.Ipv6Token)
		ctok := sMap["ipv6"]["token"]
		if ctok != nil {
			fmt.Println(ctok)
		}

		if hasPrefixAny(name, cfg.Prefixes) {
			fmt.Println(" -> Private network: applying private DNS + token")
			ipv6["dns"] = privIPv6Bytes
			ipv6["token"] = cfg.Ipv6Token
			ipv4["dns"] = privIPv4Nums
		} else if cType == "802-3-ethernet" {

			// ipv6["dns"] = privIPv6Bytes
			// ipv4["dns"] = privIPv4Nums

			delete(ipv6, "token")
			delete(ipv6, "dns")
			delete(ipv4, "dns")
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
