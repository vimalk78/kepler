// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewARMHwmonPowerMeter(t *testing.T) {
	pm, err := NewARMHwmonPowerMeter("/sys")
	assert.NoError(t, err)
	assert.NotNil(t, pm)
	assert.Equal(t, "arm-hwmon", pm.Name())
	assert.NotNil(t, pm.logger)
	assert.NotNil(t, pm.reader)
}

func TestWithARMLogger(t *testing.T) {
	logger := slog.Default().With("test", "arm-logger")
	pm, err := NewARMHwmonPowerMeter("/sys", WithARMLogger(logger))
	assert.NoError(t, err)
	assert.NotNil(t, pm.logger)
}

func TestWithHwmonReader(t *testing.T) {
	mockReader := &mockHwmonReader{
		sensors: map[string]string{
			"package": "/tmp/test-hwmon/hwmon0/power1_input",
		},
	}
	pm, err := NewARMHwmonPowerMeter("/sys", WithHwmonReader(mockReader))
	assert.NoError(t, err)
	assert.Equal(t, mockReader, pm.reader)
}

func TestARMHwmonPowerMeter_Init_NoSensors(t *testing.T) {
	mockReader := &mockHwmonReader{
		sensors: map[string]string{}, // No sensors
	}
	pm, err := NewARMHwmonPowerMeter("/sys", WithHwmonReader(mockReader))
	require.NoError(t, err)

	err = pm.Init()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no power sensors found")
}

func TestARMHwmonPowerMeter_Init_Success(t *testing.T) {
	// Create mock hwmon structure
	tempDir := t.TempDir()
	hwmonClassDir := filepath.Join(tempDir, "class", "hwmon")
	hwmonDir := filepath.Join(hwmonClassDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))

	// Create power sensors
	power1File := filepath.Join(hwmonDir, "power1_input")
	power2File := filepath.Join(hwmonDir, "power2_input")
	label1File := filepath.Join(hwmonDir, "power1_label")
	label2File := filepath.Join(hwmonDir, "power2_label")

	require.NoError(t, os.WriteFile(power1File, []byte("5000000"), 0644)) // 5W in µW
	require.NoError(t, os.WriteFile(power2File, []byte("2000000"), 0644)) // 2W in µW
	require.NoError(t, os.WriteFile(label1File, []byte("CPU Power"), 0644))
	require.NoError(t, os.WriteFile(label2File, []byte("IO Power"), 0644))

	pm, err := NewARMHwmonPowerMeter(tempDir)
	require.NoError(t, err)

	err = pm.Init()
	assert.NoError(t, err)
	assert.True(t, pm.IsInitialized())

	zones, err := pm.Zones()
	assert.NoError(t, err)
	assert.Len(t, zones, 2)

	// Check primary zone selection (CPU should have priority)
	primary, err := pm.PrimaryEnergyZone()
	assert.NoError(t, err)
	assert.Equal(t, ZonePackage, primary.Name())
}

func TestARMHwmonPowerMeter_GetZoneByName(t *testing.T) {
	// Create mock hwmon structure
	tempDir := t.TempDir()
	hwmonClassDir := filepath.Join(tempDir, "class", "hwmon")
	hwmonDir := filepath.Join(hwmonClassDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))

	power1File := filepath.Join(hwmonDir, "power1_input")
	require.NoError(t, os.WriteFile(power1File, []byte("5000000"), 0644))

	pm, err := NewARMHwmonPowerMeter(tempDir)
	require.NoError(t, err)
	require.NoError(t, pm.Init())

	zone, err := pm.GetZoneByName(ZonePackage)
	assert.NoError(t, err)
	assert.Equal(t, ZonePackage, zone.Name())

	_, err = pm.GetZoneByName("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestARMHwmonPowerMeter_GetZoneByIndex(t *testing.T) {
	// Create mock hwmon structure
	tempDir := t.TempDir()
	hwmonClassDir := filepath.Join(tempDir, "class", "hwmon")
	hwmonDir := filepath.Join(hwmonClassDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))

	power1File := filepath.Join(hwmonDir, "power1_input")
	require.NoError(t, os.WriteFile(power1File, []byte("5000000"), 0644))

	pm, err := NewARMHwmonPowerMeter(tempDir)
	require.NoError(t, err)
	require.NoError(t, pm.Init())

	zone, err := pm.GetZoneByIndex(0)
	assert.NoError(t, err)
	assert.Equal(t, 0, zone.Index())

	_, err = pm.GetZoneByIndex(99)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestARMHwmonPowerMeter_String(t *testing.T) {
	// Test uninitialized meter
	pm, err := NewARMHwmonPowerMeter("/sys")
	require.NoError(t, err)
	str := pm.String()
	assert.Contains(t, str, "ARMHwmonPowerMeter")
	assert.Contains(t, str, "error=")

	// Test initialized meter
	tempDir := t.TempDir()
	hwmonClassDir := filepath.Join(tempDir, "class", "hwmon")
	hwmonDir := filepath.Join(hwmonClassDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))
	power1File := filepath.Join(hwmonDir, "power1_input")
	require.NoError(t, os.WriteFile(power1File, []byte("5000000"), 0644))

	pm2, err := NewARMHwmonPowerMeter(tempDir)
	require.NoError(t, err)
	require.NoError(t, pm2.Init())

	str2 := pm2.String()
	assert.Contains(t, str2, "ARMHwmonPowerMeter")
	assert.Contains(t, str2, "zones=")
	assert.Contains(t, str2, "primary=")
}

func TestARMHwmonPowerMeter_Close(t *testing.T) {
	pm, err := NewARMHwmonPowerMeter("/sys")
	require.NoError(t, err)

	err = pm.Close()
	assert.NoError(t, err)
}

func TestARMHwmonPowerMeter_Init_InvalidSysfsPath(t *testing.T) {
	// Test that NewARMHwmonPowerMeter fails with invalid sysfs path
	_, err := NewARMHwmonPowerMeter("/nonexistent/path")
	assert.Error(t, err)
}

func TestARMHwmonPowerMeter_Refresh(t *testing.T) {
	// Create mock hwmon structure
	tempDir := t.TempDir()
	hwmonClassDir := filepath.Join(tempDir, "class", "hwmon")
	hwmonDir := filepath.Join(hwmonClassDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))

	power1File := filepath.Join(hwmonDir, "power1_input")
	require.NoError(t, os.WriteFile(power1File, []byte("5000000"), 0644))

	pm, err := NewARMHwmonPowerMeter(tempDir)
	require.NoError(t, err)
	require.NoError(t, pm.Init())

	err = pm.Refresh()
	assert.NoError(t, err)
}

func TestHwmonPowerScanner_ScanPowerSensors(t *testing.T) {
	tempDir := t.TempDir()
	scanner := NewHwmonPowerScanner(tempDir)

	// Test empty directory
	sensors, err := scanner.ScanPowerSensors()
	assert.NoError(t, err)
	assert.Empty(t, sensors)

	// Create mock hwmon structure
	hwmonDir := filepath.Join(tempDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))

	// Create various power sensors
	testCases := []struct {
		filename string
		label    string
		expected string
	}{
		{"power1_input", "CPU Power", ZonePackage},
		{"power2_input", "IO Power", ZoneUncore},
		{"power3_input", "", "power3"},
	}

	for _, tc := range testCases {
		powerFile := filepath.Join(hwmonDir, tc.filename)
		require.NoError(t, os.WriteFile(powerFile, []byte("1000000"), 0644))

		if tc.label != "" {
			labelFile := filepath.Join(hwmonDir, tc.filename[:len(tc.filename)-6]+"_label")
			require.NoError(t, os.WriteFile(labelFile, []byte(tc.label), 0644))
		}
	}

	// Create non-power files (should be ignored)
	require.NoError(t, os.WriteFile(filepath.Join(hwmonDir, "temp1_input"), []byte("30000"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(hwmonDir, "name"), []byte("test-hwmon"), 0644))

	sensors, err = scanner.ScanPowerSensors()
	assert.NoError(t, err)
	assert.Len(t, sensors, 3)

	for _, tc := range testCases {
		sensorPath := filepath.Join(hwmonDir, tc.filename)
		assert.Contains(t, sensors, tc.expected)
		assert.Equal(t, sensorPath, sensors[tc.expected])
	}
}

func TestHwmonPowerScanner_GetHwmonDeviceName(t *testing.T) {
	tempDir := t.TempDir()
	hwmonDir := filepath.Join(tempDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))

	scanner := NewHwmonPowerScanner(tempDir)

	// Test without name file
	name := scanner.GetHwmonDeviceName(hwmonDir)
	assert.Equal(t, "unknown", name)

	// Test with name file
	nameFile := filepath.Join(hwmonDir, "name")
	require.NoError(t, os.WriteFile(nameFile, []byte("test-device\n"), 0644))

	name = scanner.GetHwmonDeviceName(hwmonDir)
	assert.Equal(t, "test-device", name)
}

func TestDeterminePrimaryZone(t *testing.T) {
	pm := &armHwmonPowerMeter{logger: slog.Default()}

	// Test empty zones
	_, err := pm.determinePrimaryZone(nil)
	assert.Error(t, err)

	// Test single zone
	zones := []EnergyZone{
		&armEnergyZone{name: ZoneUncore, index: 0},
	}
	primary, err := pm.determinePrimaryZone(zones)
	assert.NoError(t, err)
	assert.Equal(t, ZoneUncore, primary.Name())

	// Test priority selection (Package > Uncore > Core > DRAM)
	zones = []EnergyZone{
		&armEnergyZone{name: ZoneCore, index: 0},
		&armEnergyZone{name: ZonePackage, index: 1},
		&armEnergyZone{name: ZoneDRAM, index: 2},
		&armEnergyZone{name: ZoneUncore, index: 3},
	}
	primary, err = pm.determinePrimaryZone(zones)
	assert.NoError(t, err)
	assert.Equal(t, ZonePackage, primary.Name())
	assert.Equal(t, 1, primary.Index())
}

func TestFormatSensorsList(t *testing.T) {
	pm := &armHwmonPowerMeter{}

	sensors := map[string]string{
		"cpu":  "/sys/class/hwmon/hwmon0/power1_input",
		"io":   "/sys/class/hwmon/hwmon0/power2_input",
		"dram": "/sys/class/hwmon/hwmon1/power1_input",
	}

	formatted := pm.formatSensorsList(sensors)
	assert.Len(t, formatted, 3)

	// Should be sorted
	expected := []string{
		"cpu:/sys/class/hwmon/hwmon0/power1_input",
		"dram:/sys/class/hwmon/hwmon1/power1_input",
		"io:/sys/class/hwmon/hwmon0/power2_input",
	}
	assert.Equal(t, expected, formatted)
}

func TestFormatZonesList(t *testing.T) {
	pm := &armHwmonPowerMeter{}

	zones := []EnergyZone{
		&armEnergyZone{name: ZonePackage, index: 0},
		&armEnergyZone{name: ZoneUncore, index: 1},
	}

	formatted := pm.formatZonesList(zones)
	expected := []string{"package-0", "uncore-1"}
	assert.Equal(t, expected, formatted)
}

// TestARMHwmonPowerMeter_MultipleHwmonDevices tests scanning multiple hwmon devices
func TestARMHwmonPowerMeter_MultipleHwmonDevices(t *testing.T) {
	tempDir := t.TempDir()
	hwmonClassDir := filepath.Join(tempDir, "class", "hwmon")

	// Create multiple hwmon devices
	for i := 0; i < 3; i++ {
		hwmonDir := filepath.Join(hwmonClassDir, fmt.Sprintf("hwmon%d", i))
		require.NoError(t, os.MkdirAll(hwmonDir, 0755))

		switch i {
		case 0:
			// hwmon0: CPU power
			powerFile := filepath.Join(hwmonDir, "power1_input")
			labelFile := filepath.Join(hwmonDir, "power1_label")
			require.NoError(t, os.WriteFile(powerFile, []byte("5000000"), 0644))
			require.NoError(t, os.WriteFile(labelFile, []byte("CPU Power"), 0644))
		case 1:
			// hwmon1: IO power
			powerFile := filepath.Join(hwmonDir, "power1_input")
			labelFile := filepath.Join(hwmonDir, "power1_label")
			require.NoError(t, os.WriteFile(powerFile, []byte("2000000"), 0644))
			require.NoError(t, os.WriteFile(labelFile, []byte("IO Power"), 0644))
		}
		// hwmon2: no power sensors (should be ignored)
	}

	pm, err := NewARMHwmonPowerMeter(tempDir)
	require.NoError(t, err)
	require.NoError(t, pm.Init())

	zones, err := pm.Zones()
	assert.NoError(t, err)
	assert.Len(t, zones, 2)

	// Verify zone names
	zoneNames := make([]string, len(zones))
	for i, zone := range zones {
		zoneNames[i] = zone.Name()
	}
	assert.Contains(t, zoneNames, ZonePackage)
	assert.Contains(t, zoneNames, ZoneUncore)
}

// mockHwmonReader implements hwmonReader for testing
type mockHwmonReader struct {
	sensors map[string]string
	err     error
}

// ScanPowerSensors returns mock sensor data
func (m *mockHwmonReader) ScanPowerSensors() (map[string]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.sensors, nil
}
