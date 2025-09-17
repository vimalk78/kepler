// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/prometheus/procfs/sysfs"
)

// hwmonReader is an interface for hwmon filesystem access, similar to sysfsReader in RAPL
type hwmonReader interface {
	ScanPowerSensors() (map[string]string, error)
}

// armHwmonPowerMeter implements CPUPowerMeter using hwmon power sensors for ARM systems
type armHwmonPowerMeter struct {
	name        string
	reader      hwmonReader
	cachedZones []EnergyZone
	primaryZone EnergyZone
	logger      *slog.Logger
}

// ARMPowerMeterOption defines configuration options for ARM power meter
type ARMPowerMeterOption func(*armHwmonPowerMeter)

// WithHwmonReader sets the hwmonReader used by armHwmonPowerMeter
func WithHwmonReader(r hwmonReader) ARMPowerMeterOption {
	return func(pm *armHwmonPowerMeter) {
		pm.reader = r
	}
}

// WithARMLogger sets the logger for ARM power meter
func WithARMLogger(logger *slog.Logger) ARMPowerMeterOption {
	return func(pm *armHwmonPowerMeter) {
		pm.logger = logger.With("service", "arm-power")
	}
}

// NewARMHwmonPowerMeter creates a new ARM hwmon-based CPU power meter
func NewARMHwmonPowerMeter(sysfsPath string, opts ...ARMPowerMeterOption) (*armHwmonPowerMeter, error) {
	fs, err := sysfs.NewFS(sysfsPath)
	if err != nil {
		return nil, err
	}

	pm := &armHwmonPowerMeter{
		name:   "arm-hwmon",
		reader: hwmonSysfsReader{fs: fs, sysfsPath: sysfsPath},
		logger: slog.Default().With("service", "arm-power"),
	}

	// Apply options
	for _, opt := range opts {
		opt(pm)
	}

	return pm, nil
}

// Name returns the power meter name
func (pm *armHwmonPowerMeter) Name() string {
	return pm.name
}

// Init initializes the ARM power meter by discovering available power sensors
func (pm *armHwmonPowerMeter) Init() error {
	pm.logger.Debug("Initializing ARM hwmon power meter")

	// Discover power sensors
	sensors, err := pm.reader.ScanPowerSensors()
	if err != nil {
		return fmt.Errorf("failed to scan power sensors: %w", err)
	}

	if len(sensors) == 0 {
		return fmt.Errorf("no power sensors found in hwmon")
	}

	pm.logger.Info("Discovered ARM power sensors", "count", len(sensors), "sensors", pm.formatSensorsList(sensors))

	// Create energy zones from discovered sensors
	zones, err := pm.createEnergyZones(sensors)
	if err != nil {
		return fmt.Errorf("failed to create energy zones: %w", err)
	}

	// Cache zones
	pm.cachedZones = zones

	// Determine primary zone (highest priority)
	primary, err := pm.determinePrimaryZone(zones)
	if err != nil {
		return fmt.Errorf("failed to determine primary zone: %w", err)
	}
	pm.primaryZone = primary

	pm.logger.Info("ARM power meter initialized successfully",
		"zones_count", len(zones),
		"primary_zone", fmt.Sprintf("%s-%d", primary.Name(), primary.Index()),
		"zones", pm.formatZonesList(zones))

	// Test reading from first zone to ensure it works
	_, err = zones[0].Energy()
	if err != nil {
		return fmt.Errorf("failed to read from ARM power zone %s: %w", zones[0].Name(), err)
	}

	return nil
}

// createEnergyZones creates ARM energy zones from discovered sensors
func (pm *armHwmonPowerMeter) createEnergyZones(sensors map[string]string) ([]EnergyZone, error) {
	var zones []EnergyZone
	index := 0

	// Process sensors in consistent order
	var sensorNames []string
	for name := range sensors {
		sensorNames = append(sensorNames, name)
	}
	sort.Strings(sensorNames)

	for _, zoneName := range sensorNames {
		powerPath := sensors[zoneName]

		zone, err := NewARMEnergyZone(zoneName, index, powerPath)
		if err != nil {
			pm.logger.Warn("Failed to create energy zone",
				"zone", zoneName,
				"path", powerPath,
				"error", err)
			continue
		}

		zones = append(zones, zone)
		index++

		pm.logger.Debug("Created ARM energy zone",
			"zone", zoneName,
			"index", index-1,
			"path", powerPath)
	}

	if len(zones) == 0 {
		return nil, fmt.Errorf("no valid energy zones created")
	}

	return zones, nil
}

// determinePrimaryZone selects the primary energy zone based on priority
func (pm *armHwmonPowerMeter) determinePrimaryZone(zones []EnergyZone) (EnergyZone, error) {
	if len(zones) == 0 {
		return nil, fmt.Errorf("no zones available")
	}

	// Create zone map for priority lookup
	zoneMap := make(map[string]EnergyZone)
	for _, zone := range zones {
		zoneMap[strings.ToLower(zone.Name())] = zone
	}

	// Priority order for ARM zones (highest to lowest)
	// Package (CPU) has higher priority than Uncore (IO)
	priorityOrder := []string{ZonePackage, ZoneUncore, ZoneCore, ZoneDRAM}

	// Find highest priority zone
	for _, priority := range priorityOrder {
		if zone, exists := zoneMap[priority]; exists {
			pm.logger.Debug("Selected primary zone", "zone", priority, "index", zone.Index())
			return zone, nil
		}
	}

	// Fallback to first zone
	pm.logger.Debug("Using fallback primary zone", "zone", zones[0].Name(), "index", zones[0].Index())
	return zones[0], nil
}

// Zones returns all available energy zones
func (pm *armHwmonPowerMeter) Zones() ([]EnergyZone, error) {
	if pm.cachedZones == nil {
		return nil, fmt.Errorf("power meter not initialized - call Init() first")
	}

	// Return a copy to prevent external modification
	zones := make([]EnergyZone, len(pm.cachedZones))
	copy(zones, pm.cachedZones)

	return zones, nil
}

// PrimaryEnergyZone returns the primary energy zone (highest priority)
func (pm *armHwmonPowerMeter) PrimaryEnergyZone() (EnergyZone, error) {
	if pm.primaryZone == nil {
		return nil, fmt.Errorf("power meter not initialized - call Init() first")
	}

	return pm.primaryZone, nil
}

// formatSensorsList formats discovered sensors for logging
func (pm *armHwmonPowerMeter) formatSensorsList(sensors map[string]string) []string {
	var formatted []string
	for zone, path := range sensors {
		formatted = append(formatted, fmt.Sprintf("%s:%s", zone, path))
	}
	sort.Strings(formatted)
	return formatted
}

// formatZonesList formats zones for logging
func (pm *armHwmonPowerMeter) formatZonesList(zones []EnergyZone) []string {
	var formatted []string
	for _, zone := range zones {
		formatted = append(formatted, fmt.Sprintf("%s-%d", zone.Name(), zone.Index()))
	}
	return formatted
}

// GetZoneByName returns a zone by name (for testing/debugging)
func (pm *armHwmonPowerMeter) GetZoneByName(name string) (EnergyZone, error) {
	zones, err := pm.Zones()
	if err != nil {
		return nil, err
	}

	for _, zone := range zones {
		if strings.EqualFold(zone.Name(), name) {
			return zone, nil
		}
	}

	return nil, fmt.Errorf("zone '%s' not found", name)
}

// GetZoneByIndex returns a zone by index (for testing/debugging)
func (pm *armHwmonPowerMeter) GetZoneByIndex(index int) (EnergyZone, error) {
	zones, err := pm.Zones()
	if err != nil {
		return nil, err
	}

	for _, zone := range zones {
		if zone.Index() == index {
			return zone, nil
		}
	}

	return nil, fmt.Errorf("zone with index %d not found", index)
}

// String returns a string representation of the power meter
func (pm *armHwmonPowerMeter) String() string {
	zones, err := pm.Zones()
	if err != nil {
		return fmt.Sprintf("ARMHwmonPowerMeter{name=%s, error=%v}", pm.name, err)
	}

	var zoneNames []string
	for _, zone := range zones {
		zoneNames = append(zoneNames, fmt.Sprintf("%s-%d", zone.Name(), zone.Index()))
	}

	return fmt.Sprintf("ARMHwmonPowerMeter{name=%s, zones=[%s], primary=%s-%d}",
		pm.name,
		strings.Join(zoneNames, ", "),
		pm.primaryZone.Name(),
		pm.primaryZone.Index())
}

// Close performs any necessary cleanup (implements io.Closer if needed)
func (pm *armHwmonPowerMeter) Close() error {
	pm.logger.Debug("Closing ARM power meter")
	// ARM hwmon doesn't require explicit cleanup
	return nil
}

// IsInitialized returns true if the power meter has been initialized
func (pm *armHwmonPowerMeter) IsInitialized() bool {
	return pm.cachedZones != nil && pm.primaryZone != nil
}

// Refresh forces a refresh of energy readings (useful for testing)
func (pm *armHwmonPowerMeter) Refresh() error {
	zones, err := pm.Zones()
	if err != nil {
		return err
	}

	// Trigger energy reading from all zones
	for _, zone := range zones {
		if _, err := zone.Energy(); err != nil {
			pm.logger.Warn("Failed to refresh zone", "zone", zone.Name(), "error", err)
		}
	}

	return nil
}

// hwmonSysfsReader implements hwmonReader using sysfs.FS
type hwmonSysfsReader struct {
	fs        sysfs.FS
	sysfsPath string
}

// ScanPowerSensors discovers available power sensors in hwmon using sysfs.FS
func (r hwmonSysfsReader) ScanPowerSensors() (map[string]string, error) {
	hwmonPath := filepath.Join(r.sysfsPath, "class", "hwmon")

	// Debug logging
	fmt.Printf("[DEBUG] hwmonSysfsReader: sysfsPath=%s, hwmonPath=%s\n", r.sysfsPath, hwmonPath)

	scanner := NewHwmonPowerScanner(hwmonPath)
	sensors, err := scanner.ScanPowerSensors()

	// Debug logging
	if err != nil {
		fmt.Printf("[DEBUG] hwmonSysfsReader: scan failed with error: %v\n", err)
	} else {
		fmt.Printf("[DEBUG] hwmonSysfsReader: found %d sensors: %+v\n", len(sensors), sensors)
	}

	return sensors, err
}
