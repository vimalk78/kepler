// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// armEnergyZone implements EnergyZone interface for ARM systems using hwmon
type armEnergyZone struct {
	name        string
	index       int
	powerPath   string    // Path to power*_input file
	labelPath   string    // Path to power*_label file (optional)
	label       string    // Human readable label
	lastPower   uint64    // Last power reading in µW
	lastTime    time.Time // Last reading timestamp
	accumulated Energy    // Accumulated energy in µJ
	maxEnergy   Energy    // Max energy before wraparound
	mu          sync.RWMutex
}

// NewARMEnergyZone creates a new ARM energy zone from hwmon power sensor
func NewARMEnergyZone(name string, index int, powerPath string) (EnergyZone, error) {
	// Check if power file exists
	if _, err := os.Stat(powerPath); err != nil {
		return nil, fmt.Errorf("power sensor not found: %s", powerPath)
	}

	// Generate label path from power path
	labelPath := strings.Replace(powerPath, "_input", "_label", 1)

	// Read label if available
	var label string
	if data, err := os.ReadFile(labelPath); err == nil {
		label = strings.TrimSpace(string(data))
	} else {
		// Default label based on zone name
		switch name {
		case ZonePackage:
			label = "CPU Power"
		case ZoneUncore:
			label = "IO Power"
		default:
			// Capitalize first letter manually
			if len(name) > 0 {
				label = fmt.Sprintf("%s%s Power", strings.ToUpper(name[:1]), name[1:])
			} else {
				label = "Power"
			}
		}
	}

	zone := &armEnergyZone{
		name:      name,
		index:     index,
		powerPath: powerPath,
		labelPath: labelPath,
		label:     label,
		lastTime:  time.Now(),
		// ARM systems typically don't have energy counter wraparound issues
		// Set a very high max energy value
		maxEnergy: Energy(^uint64(0) >> 1), // Max int64
	}

	// Initialize with first reading
	if err := zone.initializeReading(); err != nil {
		return nil, fmt.Errorf("failed to initialize ARM energy zone %s: %w", name, err)
	}

	return zone, nil
}

// initializeReading reads initial power value to establish baseline
func (z *armEnergyZone) initializeReading() error {
	power, err := z.readPower()
	if err != nil {
		return err
	}

	z.mu.Lock()
	z.lastPower = power
	z.lastTime = time.Now()
	z.accumulated = 0
	z.mu.Unlock()

	return nil
}

// readPower reads current power consumption from hwmon in µW
func (z *armEnergyZone) readPower() (uint64, error) {
	data, err := os.ReadFile(z.powerPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read power from %s: %w", z.powerPath, err)
	}

	powerStr := strings.TrimSpace(string(data))
	power, err := strconv.ParseUint(powerStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse power value '%s' from %s: %w", powerStr, z.powerPath, err)
	}

	return power, nil
}

// Name returns the zone name
func (z *armEnergyZone) Name() string {
	return z.name
}

// Index returns the zone index
func (z *armEnergyZone) Index() int {
	return z.index
}

// Path returns the hwmon path being read
func (z *armEnergyZone) Path() string {
	return z.powerPath
}

// Energy returns accumulated energy consumed by the zone
// This integrates instantaneous power readings over time to calculate energy
func (z *armEnergyZone) Energy() (Energy, error) {
	currentPower, err := z.readPower()
	if err != nil {
		return 0, err
	}

	currentTime := time.Now()

	z.mu.Lock()
	defer z.mu.Unlock()

	// Calculate time delta in microseconds
	timeDeltaUs := currentTime.Sub(z.lastTime).Microseconds()

	// Skip calculation if time delta is too small (< 1ms) to avoid noise
	if timeDeltaUs < 1000 {
		return z.accumulated, nil
	}

	// Calculate energy consumed since last reading
	// Power (µW) * Time (µs) = Energy (µJ)
	avgPower := (z.lastPower + currentPower) / 2 // Use average power for better accuracy
	energyDelta := avgPower * uint64(timeDeltaUs)

	// Accumulate energy
	z.accumulated += Energy(energyDelta)

	// Update state for next reading
	z.lastPower = currentPower
	z.lastTime = currentTime

	return z.accumulated, nil
}

// MaxEnergy returns the maximum energy value before wraparound
func (z *armEnergyZone) MaxEnergy() Energy {
	return z.maxEnergy
}

// Label returns the human-readable label for this zone
func (z *armEnergyZone) Label() string {
	z.mu.RLock()
	defer z.mu.RUnlock()
	return z.label
}

// CurrentPower returns the most recent power reading in µW (for debugging)
func (z *armEnergyZone) CurrentPower() (uint64, error) {
	return z.readPower()
}

// Reset resets the accumulated energy counter (useful for testing)
func (z *armEnergyZone) Reset() error {
	z.mu.Lock()
	defer z.mu.Unlock()

	power, err := z.readPower()
	if err != nil {
		return err
	}

	z.lastPower = power
	z.lastTime = time.Now()
	z.accumulated = 0

	return nil
}

// String returns a string representation of the zone
func (z *armEnergyZone) String() string {
	z.mu.RLock()
	defer z.mu.RUnlock()

	return fmt.Sprintf("ARMZone{name=%s, index=%d, path=%s, label=%s, energy=%s}",
		z.name, z.index, z.powerPath, z.label, z.accumulated)
}

// hwmonPowerScanner helps discover power sensors in hwmon
type hwmonPowerScanner struct {
	hwmonPath string
}

// NewHwmonPowerScanner creates a new hwmon power sensor scanner
func NewHwmonPowerScanner(hwmonPath string) *hwmonPowerScanner {
	return &hwmonPowerScanner{hwmonPath: hwmonPath}
}

// ScanPowerSensors discovers available power sensors in hwmon
func (s *hwmonPowerScanner) ScanPowerSensors() (map[string]string, error) {
	sensors := make(map[string]string)

	// Read hwmon directory
	entries, err := os.ReadDir(s.hwmonPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read hwmon directory %s: %w", s.hwmonPath, err)
	}

	// Scan each hwmon device
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "hwmon") {
			continue
		}

		hwmonDir := fmt.Sprintf("%s/%s", s.hwmonPath, entry.Name())

		// Check for power sensors in this hwmon device
		powerSensors, err := s.scanHwmonDevice(hwmonDir)
		if err != nil {
			continue // Skip devices we can't read
		}

		// Add discovered sensors
		for sensorName, sensorPath := range powerSensors {
			sensors[sensorName] = sensorPath
		}
	}

	return sensors, nil
}

// scanHwmonDevice scans a specific hwmon device for power sensors
func (s *hwmonPowerScanner) scanHwmonDevice(hwmonDir string) (map[string]string, error) {
	sensors := make(map[string]string)

	entries, err := os.ReadDir(hwmonDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, "power") && strings.HasSuffix(name, "_input") {
			powerPath := fmt.Sprintf("%s/%s", hwmonDir, name)

			// Determine sensor type from label or default naming
			var sensorName string
			labelPath := strings.Replace(powerPath, "_input", "_label", 1)

			if labelData, err := os.ReadFile(labelPath); err == nil {
				label := strings.TrimSpace(string(labelData))
				// Map common labels to zone types
				switch strings.ToLower(label) {
				case "cpu power":
					sensorName = ZonePackage
				case "io power":
					sensorName = ZoneUncore
				default:
					// Use the power sensor number as fallback
					if strings.Contains(name, "power1") {
						sensorName = ZonePackage
					} else if strings.Contains(name, "power2") {
						sensorName = ZoneUncore
					} else {
						sensorName = fmt.Sprintf("power%s", strings.TrimPrefix(strings.TrimSuffix(name, "_input"), "power"))
					}
				}
			} else {
				// No label file, use sensor number mapping
				if strings.Contains(name, "power1") {
					sensorName = ZonePackage
				} else if strings.Contains(name, "power2") {
					sensorName = ZoneUncore
				} else {
					sensorName = fmt.Sprintf("power%s", strings.TrimPrefix(strings.TrimSuffix(name, "_input"), "power"))
				}
			}

			sensors[sensorName] = powerPath
		}
	}

	return sensors, nil
}

// GetHwmonDeviceName returns the name of hwmon device (for logging)
func (s *hwmonPowerScanner) GetHwmonDeviceName(hwmonDir string) string {
	namePath := fmt.Sprintf("%s/name", hwmonDir)
	if data, err := os.ReadFile(namePath); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "unknown"
}
