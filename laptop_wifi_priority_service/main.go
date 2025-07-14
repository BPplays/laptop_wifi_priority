package main

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	loopInterval   = 30 * time.Second
	scanThreshold  = 29 * time.Second
)

// getKnownNetworks retrieves SSIDs of saved Wi-Fi connections via NetworkManager D-Bus
func getKnownNetworks() ([]string, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	nmSettings := conn.Object("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager/Settings")
	var paths []dbus.ObjectPath
	if err := nmSettings.Call("org.freedesktop.NetworkManager.Settings.ListConnections", 0).Store(&paths); err != nil {
		return nil, err
	}

	var ssids []string
	for _, path := range paths {
		obj := conn.Object("org.freedesktop.NetworkManager", path)
		var settings map[string]map[string]dbus.Variant
		if err := obj.Call("org.freedesktop.NetworkManager.Settings.Connection.GetSettings", 0).Store(&settings); err != nil {
			continue
		}
		if settings["connection"]["type"].Value().(string) != "802-11-wireless" {
			continue
		}
		ssidVar := settings["802-11-wireless"]["ssid"]
		// convert []byte to string
		b := ssidVar.Value().([]byte)
		ssid := string(b)

		// check timestamp exists
		if _, ok := settings["connection"]["timestamp"]; ok {
			ssids = append(ssids, ssid)
		}
	}
	return ssids, nil
}

// getCurrentConnection returns the SSID of the active Wi-Fi network
func getCurrentConnection() (string, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return "", err
	}
	nm := conn.Object("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager")
	var devPaths []dbus.ObjectPath
	if err := nm.Call("org.freedesktop.NetworkManager.GetDevices", 0).Store(&devPaths); err != nil {
		return "", err
	}
	for _, path := range devPaths {
		dev := conn.Object("org.freedesktop.NetworkManager", path)
		var devType uint32
		_ = dev.GetProperty("org.freedesktop.NetworkManager.Device.DeviceType").Store(&devType)
		if devType != 2 {
			continue
		}
		var state uint32
		_ = dev.GetProperty("org.freedesktop.NetworkManager.Device.State").Store(&state)
		if state != 100 {
			continue
		}
		// active AP
		var apPath dbus.ObjectPath
		_ = dev.GetProperty("org.freedesktop.NetworkManager.Device.Wireless.ActiveAccessPoint").Store(&apPath)
		if apPath == "" {
			continue
		}
		ap := conn.Object("org.freedesktop.NetworkManager", apPath)
		var ssid []byte
		_ = ap.GetProperty("org.freedesktop.NetworkManager.AccessPoint.Ssid").Store(&ssid)
		return string(ssid), nil
	}
	return "", nil
}

// getWifiNetworks scans available Wi-Fi networks
func getWifiNetworks() ([]map[string]interface{}, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, err
	}
	nm := conn.Object("org.freedesktop.NetworkManager", "/org/freedesktop/NetworkManager")
	var devPaths []dbus.ObjectPath
	if err := nm.Call("org.freedesktop.NetworkManager.GetDevices", 0).Store(&devPaths); err != nil {
		return nil, err
	}

	var nets []map[string]interface{}
	for _, path := range devPaths {
		dev := conn.Object("org.freedesktop.NetworkManager", path)
		var devType uint32
		_ = dev.GetProperty("org.freedesktop.NetworkManager.Device.DeviceType").Store(&devType)
		if devType != 2 {
			continue
		}
		wifi := conn.Object("org.freedesktop.NetworkManager", path)
		var apPaths []dbus.ObjectPath
		_ = wifi.Call("org.freedesktop.NetworkManager.Device.Wireless.GetAccessPoints", 0).Store(&apPaths)
		for _, apPath := range apPaths {
			ap := conn.Object("org.freedesktop.NetworkManager", apPath)
			var ssid []byte
			var freq uint32
			var strength uint8
			_ = ap.GetProperty("org.freedesktop.NetworkManager.AccessPoint.Ssid").Store(&ssid)
			_ = ap.GetProperty("org.freedesktop.NetworkManager.AccessPoint.Frequency").Store(&freq)
			_ = ap.GetProperty("org.freedesktop.NetworkManager.AccessPoint.Strength").Store(&strength)
			nets = append(nets, map[string]interface{}{
				"ssid":      string(ssid),
				"frequency": freq,
				"strength":  strength,
				"isKnown":   false,
			})
		}
	}
	return nets, nil
}

// convertBootTime converts NM LastScan boot time ms to Unix timestamp
func convertBootTime(ms uint64) time.Time {
	bootNow := time.Now().UnixNano()/1e9 - int64(time.Since(time.Now()))
	return time.Unix(0, int64(ms)*int64(time.Millisecond)).Add(time.Duration(bootNow) * time.Second)
}

// requestRescan triggers a Wi-Fi scan if threshold exceeded
func requestRescan() error {
	// Simplified: always request scan
	cmd := exec.Command("nmcli", "device", "wifi", "rescan")
	return cmd.Run()
}

// connectToSSID uses nmcli to connect
func connectToSSID(ssid string, password ...string) error {
	args := []string{"dev", "wifi", "connect", ssid}
	if len(password) > 0 {
		args = append(args, "password", password[0])
	}
	cmd := exec.Command("nmcli", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("connect error: %v: %s", err, string(out))
	}
	fmt.Println(string(out))
	return nil
}

func main() {
	for {
		fmt.Println("Starting loop")

		if err := requestRescan(); err != nil {
			fmt.Println("Rescan failed:", err)
		}

		known, _ := getKnownNetworks()
		nets, _ := getWifiNetworks()
		current, _ := getCurrentConnection()

		// Mark known
		for i := range nets {
			ssid := nets[i]["ssid"].(string)
			for _, k := range known {
				if ssid == k {
					nets[i]["isKnown"] = true
				}
			}
		}

		// Sort logic omitted for brevity

		// Attempt connect
		for _, netw := range nets {
			s := netw["ssid"].(string)
			if s != current {
				fmt.Printf("Connecting to %s...\n", s)
				err := connectToSSID(s)
				if err == nil {
					break
				}
			}
		}

		time.Sleep(loopInterval)
	}
}
