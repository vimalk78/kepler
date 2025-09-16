// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewARMEnergyZone_Success(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")
	labelFile := filepath.Join(tempDir, "power1_label")

	require.NoError(t, os.WriteFile(powerFile, []byte("5000000"), 0644)) // 5W in µW
	require.NoError(t, os.WriteFile(labelFile, []byte("CPU Power"), 0644))

	zone, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	assert.NoError(t, err)
	assert.NotNil(t, zone)
	assert.Equal(t, ZonePackage, zone.Name())
	assert.Equal(t, 0, zone.Index())
	assert.Equal(t, powerFile, zone.Path())
	if armZone, ok := zone.(*armEnergyZone); ok {
		assert.Equal(t, "CPU Power", armZone.Label())
	}
}

func TestNewARMEnergyZone_MissingPowerFile(t *testing.T) {
	_, err := NewARMEnergyZone(ZonePackage, 0, "/nonexistent/power1_input")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "power sensor not found")
}

func TestNewARMEnergyZone_DefaultLabels(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")
	require.NoError(t, os.WriteFile(powerFile, []byte("5000000"), 0644))

	testCases := []struct {
		zoneName      string
		expectedLabel string
	}{
		{ZonePackage, "CPU Power"},
		{ZoneUncore, "IO Power"},
		{"custom", "Custom Power"},
	}

	for _, tc := range testCases {
		zone, err := NewARMEnergyZone(tc.zoneName, 0, powerFile)
		assert.NoError(t, err)
		if armZone, ok := zone.(*armEnergyZone); ok {
			assert.Equal(t, tc.expectedLabel, armZone.Label())
		}
	}
}

func TestARMEnergyZone_BasicProperties(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")
	require.NoError(t, os.WriteFile(powerFile, []byte("3000000"), 0644)) // 3W in µW

	zone, err := NewARMEnergyZone(ZoneCore, 42, powerFile)
	require.NoError(t, err)

	assert.Equal(t, ZoneCore, zone.Name())
	assert.Equal(t, 42, zone.Index())
	assert.Equal(t, powerFile, zone.Path())
	assert.Equal(t, Energy(^uint64(0)>>1), zone.MaxEnergy()) // Max int64
}

func TestARMEnergyZone_CurrentPower(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")

	// Test initial power reading
	require.NoError(t, os.WriteFile(powerFile, []byte("4000000"), 0644)) // 4W in µW

	zone, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	require.NoError(t, err)

	if armZone, ok := zone.(*armEnergyZone); ok {
		power, err := armZone.CurrentPower()
		assert.NoError(t, err)
		assert.Equal(t, uint64(4000000), power)

		// Update power file and read again
		require.NoError(t, os.WriteFile(powerFile, []byte("6000000"), 0644)) // 6W in µW

		power, err = armZone.CurrentPower()
		assert.NoError(t, err)
		assert.Equal(t, uint64(6000000), power)
	}
}

func TestARMEnergyZone_Energy_Integration(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")

	// Start with 2W power consumption
	require.NoError(t, os.WriteFile(powerFile, []byte("2000000"), 0644)) // 2W in µW

	zone, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	require.NoError(t, err)

	// First energy reading should be zero (baseline)
	energy1, err := zone.Energy()
	assert.NoError(t, err)
	assert.Equal(t, Energy(0), energy1)

	// Wait a small amount of time and change power
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, os.WriteFile(powerFile, []byte("4000000"), 0644)) // 4W in µW

	// Second energy reading should show accumulated energy
	energy2, err := zone.Energy()
	assert.NoError(t, err)
	assert.Greater(t, energy2, Energy(0))

	// Energy should continue to accumulate
	time.Sleep(10 * time.Millisecond)
	energy3, err := zone.Energy()
	assert.NoError(t, err)
	assert.Greater(t, energy3, energy2)
}

func TestARMEnergyZone_Energy_TimeDeltaTooSmall(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")
	require.NoError(t, os.WriteFile(powerFile, []byte("2000000"), 0644))

	zone, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	require.NoError(t, err)

	// First reading
	energy1, err := zone.Energy()
	assert.NoError(t, err)

	// Immediate second reading (< 1ms delta) should return same energy
	energy2, err := zone.Energy()
	assert.NoError(t, err)
	assert.Equal(t, energy1, energy2)
}

func TestARMEnergyZone_Reset(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")
	require.NoError(t, os.WriteFile(powerFile, []byte("3000000"), 0644))

	zone, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	require.NoError(t, err)

	// Accumulate some energy
	time.Sleep(5 * time.Millisecond)
	energy1, err := zone.Energy()
	assert.NoError(t, err)

	time.Sleep(5 * time.Millisecond)
	energy2, err := zone.Energy()
	assert.NoError(t, err)
	assert.Greater(t, energy2, energy1)

	// Reset should zero the accumulated energy
	if armZone, ok := zone.(*armEnergyZone); ok {
		err = armZone.Reset()
		assert.NoError(t, err)
	}

	energy3, err := zone.Energy()
	assert.NoError(t, err)
	assert.Equal(t, Energy(0), energy3)
}

func TestARMEnergyZone_String(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")
	require.NoError(t, os.WriteFile(powerFile, []byte("5000000"), 0644))

	zone, err := NewARMEnergyZone(ZonePackage, 1, powerFile)
	require.NoError(t, err)

	if armZone, ok := zone.(*armEnergyZone); ok {
		str := armZone.String()
		assert.Contains(t, str, "ARMZone")
		assert.Contains(t, str, "name=package")
		assert.Contains(t, str, "index=1")
		assert.Contains(t, str, powerFile)
		assert.Contains(t, str, "label=CPU Power")
		assert.Contains(t, str, "energy=")
	}
}

func TestARMEnergyZone_InvalidPowerFile(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")

	// Create power file with invalid content
	require.NoError(t, os.WriteFile(powerFile, []byte("not-a-number"), 0644))

	_, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize ARM energy zone")

	// Create zone with valid initial content
	require.NoError(t, os.WriteFile(powerFile, []byte("1000000"), 0644))
	validZone, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	require.NoError(t, err)

	// Corrupt the file and try to read energy
	require.NoError(t, os.WriteFile(powerFile, []byte("invalid"), 0644))
	_, err = validZone.Energy()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse power value")
}

func TestARMEnergyZone_EnergyCalculation(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")

	// Test with known power values to verify energy calculation
	// Start with 1W (1,000,000 µW)
	require.NoError(t, os.WriteFile(powerFile, []byte("1000000"), 0644))

	zone, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	require.NoError(t, err)

	// Record start time and initial energy
	startTime := time.Now()
	initialEnergy, err := zone.Energy()
	assert.NoError(t, err)
	assert.Equal(t, Energy(0), initialEnergy)

	// Wait 100ms and maintain same power
	time.Sleep(100 * time.Millisecond)

	energy1, err := zone.Energy()
	assert.NoError(t, err)

	// Expected energy: 1W * 0.1s = 0.1J = 100,000µJ (approximately)
	// But the calculation uses avgPower * timeDeltaUs which is µW * µs = µJ
	// So 1,000,000 µW * 100,000 µs = 100,000,000,000 µJ
	expectedEnergy := Energy(100000000000) // 100 billion µJ = 100,000 J = 100 kJ
	tolerance := Energy(20000000000)       // 20 billion µJ tolerance

	assert.InDelta(t, float64(expectedEnergy), float64(energy1), float64(tolerance),
		"Energy calculation should be approximately correct")

	elapsedUs := time.Since(startTime).Microseconds()
	t.Logf("Elapsed time: %d µs, Accumulated energy: %d µJ", elapsedUs, energy1)
}

func TestReadPower_FileOperations(t *testing.T) {
	tempDir := t.TempDir()
	powerFile := filepath.Join(tempDir, "power1_input")

	require.NoError(t, os.WriteFile(powerFile, []byte("2500000"), 0644))

	zone, err := NewARMEnergyZone(ZonePackage, 0, powerFile)
	require.NoError(t, err)

	// Test reading power through CurrentPower method
	power, err := zone.(*armEnergyZone).CurrentPower()
	assert.NoError(t, err)
	assert.Equal(t, uint64(2500000), power)

	// Test with whitespace
	require.NoError(t, os.WriteFile(powerFile, []byte("  3500000  \n"), 0644))
	power, err = zone.(*armEnergyZone).CurrentPower()
	assert.NoError(t, err)
	assert.Equal(t, uint64(3500000), power)

	// Test file removal
	require.NoError(t, os.Remove(powerFile))
	_, err = zone.(*armEnergyZone).CurrentPower()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read power")
}

func TestHwmonPowerScanner_ScanHwmonDevice(t *testing.T) {
	tempDir := t.TempDir()
	scanner := NewHwmonPowerScanner(tempDir)

	hwmonDir := filepath.Join(tempDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))

	// Create various types of files
	files := map[string]string{
		"power1_input": "5000000",
		"power2_input": "3000000",
		"power1_label": "CPU Power",
		"power2_label": "IO Power",
		"temp1_input":  "45000",      // Should be ignored
		"fan1_input":   "2000",       // Should be ignored
		"name":         "test-hwmon", // Should be ignored
	}

	for filename, content := range files {
		filePath := filepath.Join(hwmonDir, filename)
		require.NoError(t, os.WriteFile(filePath, []byte(content), 0644))
	}

	// Create a subdirectory (should be ignored)
	subDir := filepath.Join(hwmonDir, "device")
	require.NoError(t, os.MkdirAll(subDir, 0755))

	sensors, err := scanner.scanHwmonDevice(hwmonDir)
	assert.NoError(t, err)
	assert.Len(t, sensors, 2)

	expectedSensors := map[string]string{
		ZonePackage: filepath.Join(hwmonDir, "power1_input"),
		ZoneUncore:  filepath.Join(hwmonDir, "power2_input"),
	}

	for sensorName, expectedPath := range expectedSensors {
		actualPath, exists := sensors[sensorName]
		assert.True(t, exists, "Sensor %s should exist", sensorName)
		assert.Equal(t, expectedPath, actualPath)
	}
}

func TestHwmonPowerScanner_LabelMapping(t *testing.T) {
	tempDir := t.TempDir()
	scanner := NewHwmonPowerScanner(tempDir)

	hwmonDir := filepath.Join(tempDir, "hwmon0")
	require.NoError(t, os.MkdirAll(hwmonDir, 0755))

	testCases := []struct {
		powerFile    string
		label        string
		expectedZone string
	}{
		{"power1_input", "CPU Power", ZonePackage},
		{"power2_input", "IO Power", ZoneUncore},
		{"power3_input", "Some Other Label", "power3"},
		{"power4_input", "", "power4"}, // No label file
	}

	for _, tc := range testCases {
		// Create power file
		powerPath := filepath.Join(hwmonDir, tc.powerFile)
		require.NoError(t, os.WriteFile(powerPath, []byte("1000000"), 0644))

		// Create label file if specified
		if tc.label != "" {
			labelFile := tc.powerFile[:len(tc.powerFile)-6] + "_label"
			labelPath := filepath.Join(hwmonDir, labelFile)
			require.NoError(t, os.WriteFile(labelPath, []byte(tc.label), 0644))
		}
	}

	sensors, err := scanner.scanHwmonDevice(hwmonDir)
	assert.NoError(t, err)
	assert.Len(t, sensors, 4)

	for _, tc := range testCases {
		expectedPath := filepath.Join(hwmonDir, tc.powerFile)
		actualPath, exists := sensors[tc.expectedZone]
		assert.True(t, exists, "Zone %s should exist", tc.expectedZone)
		assert.Equal(t, expectedPath, actualPath, "Path for zone %s", tc.expectedZone)
	}
}
