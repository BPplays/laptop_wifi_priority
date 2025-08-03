// wifi_manager.go
package main

import (
	"fmt"
	"log"
	"sort"
	"time"
	"strings"
	"os"
	"os/exec"
	"bytes"

	gonm "github.com/Wifx/gonetworkmanager"
	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const (
	LoopInterval   = 30 * time.Second
	ScanThreshold  = 29 * time.Second
)

// convertClockBootTimeToUnix converts D-Bus CLOCK_BOOTTIME ms timestamp to Unix time
func convertClockBootTimeToUnix(clockBootMs uint64) (time.Time, error) {
	// get CLOCK_BOOTTIME in seconds
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return time.Time{}, err
	}
	// current monotonic seconds since boot
	bootSec := float64(ts.Sec) + float64(ts.Nsec)/1e9
	// current Unix time
	now := time.Now()
	// compute boot base unix = now - bootSec
	base := now.Add(-time.Duration(bootSec * float64(time.Second)))
	// add the clockBootMs
	return base.Add(time.Duration(clockBootMs) * time.Millisecond), nil
}

// getKnownNetworks returns SSIDs of saved Wi-Fi connections that have been used
func getKnownNetworks() ([]string, error) {
	settings, err := gonm.NewSettings()
	if err != nil {
		return nil, err
	}
	conns, err := settings.ListConnections()
	if err != nil {
		return nil, err
	}
	var known []string
	for _, conn := range conns {
		cs, err := conn.GetSettings()
		if err != nil {
			continue
		}
		if cs["connection"]["type"] == "802-11-wireless" {
			ssidBytes := cs["802-11-wireless"]["ssid"].([]uint8)
			ssid := string(ssidBytes)
			// check timestamp key
			if _, ok := cs["connection"]["timestamp"]; ok {
				known = append(known, ssid)
			}
		}
	}
	return known, nil
}

// getCurrentConnection returns the SSID of the active Wi-Fi network
func getCurrentConnection() (string, error) {
	nm, err := gonm.NewNetworkManager()
	if err != nil {
		return "", err
	}
	devs, err := nm.GetDevices()
	if err != nil {
		return "", err
	}
	for _, d := range devs {
		t, err := d.GetPropertyDeviceType()
		if err != nil || t != gonm.NmDeviceTypeWifi {
			continue
		}
		state, err := d.GetPropertyState()
		if err != nil || state != gonm.NmDeviceStateActivated {
			continue
		}
		dw, err := gonm.NewDeviceWireless(d.GetPath())
		if err != nil {
			continue
		}
		ap, err := dw.GetPropertyActiveAccessPoint()
		if err != nil || ap == nil {
			continue
		}
		ssid, err := ap.GetPropertySSID()
		if err != nil {
			return "", err
		}
		return ssid, nil
	}
	return "", nil
}

// getWifiNetworks scans and returns available Wi-Fi networks
func getWifiNetworks() ([]map[string]interface{}, error) {
	nm, err := gonm.NewNetworkManager()
	if err != nil {
		return nil, err
	}
	devs, err := nm.GetDevices()
	if err != nil {
		return nil, err
	}
	var list []map[string]interface{}
	for _, d := range devs {
		t, err := d.GetPropertyDeviceType()
		if err != nil || t != gonm.NmDeviceTypeWifi {
			continue
		}
		dw, err := gonm.NewDeviceWireless(d.GetPath())
		if err != nil {
			continue
		}
		aps, err := dw.GetAccessPoints()
		if err != nil {
			continue
		}
		for _, ap := range aps {
			ssid, _ := ap.GetPropertySSID()
			freq, _ := ap.GetPropertyFrequency()
			strength, _ := ap.GetPropertyStrength()
			list = append(list, map[string]interface{}{
				"ssid":      ssid,
				"frequency": freq,
				"strength":  strength,
				"isKnown":   false,
			})
		}
	}
	return list, nil
}

func setWiFiPriority(conns []dbus.ObjectPath, priority int32) {
	// Connect to the system bus
	conn, err := dbus.SystemBus()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to system bus: %v\n", err)
		return
	}

	for _, objPath := range conns {
		fmt.Printf("Trying to set priority for %s to %d\n", objPath, priority)

		obj := conn.Object("org.freedesktop.NetworkManager", objPath)

		// 1) Retrieve current settings
		var settings map[string]map[string]dbus.Variant
		getCall := obj.Call(
			"org.freedesktop.NetworkManager.Settings.Connection.GetSettings",
			0,
		)
		if getCall.Err != nil {
			fmt.Printf("  → GetSettings failed for %s: %v\n", objPath, getCall.Err)
			continue
		}
		if err := getCall.Store(&settings); err != nil {
			fmt.Printf("  → Could not parse settings for %s: %v\n", objPath, err)
			continue
		}

		// 2) Update autoconnect-priority
		connGrp, ok := settings["connection"]
		if !ok {
			connGrp = make(map[string]dbus.Variant)
		}
		connGrp["autoconnect-priority"] = dbus.MakeVariant(priority)
		settings["connection"] = connGrp

		// 3) Push updated settings back
		updateCall := obj.Call(
			"org.freedesktop.NetworkManager.Settings.Connection.Update",
			0,
			settings,
		)
		if updateCall.Err != nil {
			fmt.Printf("  → Update failed for %s: %v\n", objPath, updateCall.Err)
			continue
		}

		fmt.Printf("  ✓ Priority set for %s to %d\n", objPath, priority)
	}

	fmt.Println("All connections processed.")
}

// getConnectionsBySSID finds saved connection paths for an SSID
func getConnectionsBySSID(ssid string) ([]dbus.ObjectPath, error) {
	settingsObj := dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")
	bus, _ := dbus.SystemBus()
	obj := bus.Object("org.freedesktop.NetworkManager", settingsObj)
	var paths []dbus.ObjectPath
	obj.Call("ListConnections", 0).Store(&paths)
	var matches []dbus.ObjectPath
	for _, p := range paths {
		cobj := bus.Object("org.freedesktop/NetworkManager", p)
		iface := dbus.NewInterface(cobj, "org.freedesktop.NetworkManager.Settings.Connection")
		var cs map[string]map[string]interface{}
		iface.Call("GetSettings", 0).Store(&cs)
		if cs["connection"]["type"] == "802-11-wireless" {
			b := cs["802-11-wireless"]["ssid"].([]uint8)
			if string(b) == ssid {
				matches = append(matches, p)
			}
		}
	}
	return matches, nil
}

// getFrequencyPriority assigns weight by SSID keywords
func getFrequencyPriority(ssid string) int {
	l := strings.ToLower(ssid)
	switch {
	case strings.Contains(l, "6ghz"), strings.Contains(l, "6g"):
		return 60
	case strings.Contains(l, "5ghz"), strings.Contains(l, "5g"):
		return 50
	case strings.Contains(l, "2.4ghz"), strings.Contains(l, "2ghz"), strings.Contains(l, "2g"):
		return 24
	}
	return 0
}

// getStraPuni returns 100 if network qualifies by signal or is current
func getStraPuni(strength uint8, sigMin int, ssid, current string) int {
	if int(strength) >= sigMin || ssid == current {
		return 100
	}
	return 0
}

func connectToSSID(ssid, password string) bool {
	// Build base command arguments
	args := []string{"dev", "wifi", "connect", ssid}
	if password != "" {
		args = append(args, "password", password)
	}

	// Prepare the command
	cmd := exec.Command("nmcli", args...)

	// Buffers to capture stdout and stderr
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	// Run it
	if err := cmd.Run(); err != nil {
		fmt.Printf("Failed to connect to %s: %s\n", ssid, errBuf.String())
		return false
	}

	// Success
	fmt.Print(outBuf.String())
	return true
}

func printWifin(net map[string]interface{}) {
	fmt.Printf("%s:\n", net["ssid"])
	fmt.Printf("    frequency: %v\n", net["frequency"])
	fmt.Printf("    strength:  %v\n", net["strength"])
	fmt.Printf("    isKnown:   %v\n", net["isKnown"])
}

func main() {
	var noNetExtra_sleep time.Duration = 0
	for {
		fmt.Println("Starting scan logic...")
		// Rescan logic
		// (Omitted for brevity: you can call RequestScan similarly to getAccessPoints)

		known, _ := getKnownNetworks()
		avail, _ := getWifiNetworks()
		current, _ := getCurrentConnection()
		sigMin := 15

		// mark known
		for _, net := range avail {
			for _, ks := range known {
				if net["ssid"] == ks {
					net["isKnown"] = true
				}
			}
		}

		// sort
		sort.Slice(avail, func(i, j int) bool {

			a, b := avail[i], avail[j]
			// known first
			aKnown := a["isKnown"].(bool)
			bKnown := b["isKnown"].(bool)
			if aKnown != bKnown {
				return aKnown
			}
			// signal
			aStr := int(a["strength"].(uint8))
			bStr := int(b["strength"].(uint8))
			if aStr != bStr {
				return aStr > bStr
			}
			// frequency priority
			aFreq := getFrequencyPriority(a["ssid"].(string))
			bFreq := getFrequencyPriority(b["ssid"].(string))
			return aFreq > bFreq
		})

		fmt.Println("Available networks:")
		for _, net := range avail {
			printWifin(net)
		}

		// set priorities
		for idx, net := range avail {
			paths, _ := getConnectionsBySSID(net["ssid"].(string))
			prio := int32(len(avail) - idx + 10)
			setWiFiPriority(paths, prio)
		}

		// connect to best
		if len(avail) > 0 {
			noNetExtra_sleep = 0

			best := avail[0]["ssid"].(string)
			if best != current {
				paths, _ := getConnectionsBySSID(best)
				if len(paths) > 0 {
					// activate first match
					nm, _ := gonm.NewNetworkManager()
					devs, _ := nm.GetDevices()
					// find wifi device
					for _, d := range devs {
						t, _ := d.GetPropertyDeviceType()
						if t != gonm.NmDeviceTypeWifi {
							continue
						}
						dw, _ := gonm.NewDeviceWireless(d.GetPath())
						aps, _ := dw.GetAccessPoints()
						// find AP matching SSID
						var target *gonm.AccessPoint
						for _, ap := range aps {
							if ss, _ := ap.GetPropertySSID(); ss == best {
								target = &ap
								break
							}
						}
						if target != nil {
							conn, _ := gonm.NewConnection(paths[0])
							nm.ActivateWirelessConnection(conn, d, *target)
							fmt.Println("Connecting to", best)
						}
					}
				}
			}
		} else {
			log.Println("no networks found.")
			time.Sleep(noNetExtra_sleep)

			if noNetExtra_sleep < 500 * time.Millisecond {
				noNetExtra_sleep = 500 * time.Millisecond
			}

			noNetExtra_sleep = time.Duration(float64(noNetExtra_sleep) * 1.2)

			if noNetExtra_sleep > 100 * time.Second {
				noNetExtra_sleep = 100 * time.Second
			}
		}

		// sleep
		time.Sleep(LoopInterval)
	}
}
