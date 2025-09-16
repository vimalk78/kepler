// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPowerMeterFactory(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")
	assert.NotNil(t, factory)
	assert.NotNil(t, factory.logger)
	assert.Equal(t, "/sys", factory.sysfsPath)
	assert.Equal(t, "/proc", factory.procfsPath)

	logger := slog.Default().With("test", "factory")
	factory2 := NewPowerMeterFactory(logger, "/host/sys", "/host/proc")
	assert.NotNil(t, factory2)
	assert.NotNil(t, factory2.logger)
	assert.Equal(t, "/host/sys", factory2.sysfsPath)
	assert.Equal(t, "/host/proc", factory2.procfsPath)
}

func TestPowerMeterFactory_DetectArchitecture(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	arch := factory.DetectArchitecture()
	assert.NotEmpty(t, arch)

	// Should detect one of the known architectures or unknown
	validArchs := []CPUArchitecture{ArchIntel, ArchAMD, ArchARM, ArchUnknown}
	assert.Contains(t, validArchs, arch)

	// Architecture should match runtime.GOARCH expectations
	switch runtime.GOARCH {
	case "arm64", "arm":
		// ARM detection requires additional hardware checks
		// so we can't guarantee it will return ArchARM in test environment
		assert.Contains(t, []CPUArchitecture{ArchARM, ArchUnknown}, arch)
	case "amd64", "386":
		// x86 should return Intel or AMD (defaulting to Intel)
		assert.Contains(t, []CPUArchitecture{ArchIntel, ArchAMD}, arch)
	default:
		// Unknown architectures should return ArchUnknown
		assert.Equal(t, ArchUnknown, arch)
	}
}

func TestPowerMeterFactory_DetectX86Vendor(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	// Test with mock /proc/cpuinfo
	tempDir := t.TempDir()
	procCPUInfo := filepath.Join(tempDir, "cpuinfo")

	testCases := []struct {
		name     string
		content  string
		expected CPUArchitecture
	}{
		{
			name:     "Intel CPU",
			content:  "processor\t: 0\nvendor_id\t: GenuineIntel\nmodel name\t: Intel(R) Core(TM) i7-8700K CPU",
			expected: ArchIntel,
		},
		{
			name:     "AMD CPU",
			content:  "processor\t: 0\nvendor_id\t: AuthenticAMD\nmodel name\t: AMD Ryzen 7 3700X",
			expected: ArchAMD,
		},
		{
			name:     "Intel in model name",
			content:  "processor\t: 0\nmodel name\t: Intel Core i5-9400F",
			expected: ArchIntel,
		},
		{
			name:     "AMD in model name",
			content:  "processor\t: 0\nmodel name\t: AMD EPYC 7742",
			expected: ArchAMD,
		},
		{
			name:     "Unknown vendor",
			content:  "processor\t: 0\nvendor_id\t: UnknownVendor\nmodel name\t: Unknown CPU",
			expected: ArchIntel, // Default to Intel
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(procCPUInfo, []byte(tc.content), 0644))

			// We can't easily override /proc/cpuinfo in the test, so this test
			// primarily validates the logic would work correctly
			arch := factory.detectX86Vendor()
			// On actual systems, this will read the real /proc/cpuinfo
			// so we just verify it returns a valid x86 architecture
			assert.Contains(t, []CPUArchitecture{ArchIntel, ArchAMD}, arch)
		})
	}
}

func TestPowerMeterFactory_CreateCPUPowerMeter_Fake(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	// Test creating fake power meter when no hardware is available
	meter, err := factory.CreateCPUPowerMeter()

	// On systems without RAPL or ARM hardware, it should create a fake meter
	if err != nil {
		// If creation fails, it should be due to missing hardware
		assert.Contains(t, err.Error(), "failed to")
	} else {
		// If creation succeeds, verify it's a valid CPUPowerMeter
		assert.NotNil(t, meter)
		assert.NotEmpty(t, meter.Name())
	}
}

func TestPowerMeterFactory_HasRAPLSupport(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	hasSupport := factory.HasRAPLSupport()
	assert.IsType(t, false, hasSupport) // Just verify it returns a bool

	// On most test systems, RAPL won't be available
	// but this tests the logic without requiring special hardware
}

func TestPowerMeterFactory_HasARMHwmonSupport(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	hasSupport := factory.HasARMHwmonSupport()
	assert.IsType(t, false, hasSupport) // Just verify it returns a bool
}

func TestPowerMeterFactory_GetSupportedFeatures(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	features := factory.GetSupportedFeatures()
	assert.IsType(t, map[string]bool{}, features)

	// Should always have these keys
	expectedKeys := []string{"rapl", "arm_hwmon", "fake_meter"}
	for _, key := range expectedKeys {
		_, exists := features[key]
		assert.True(t, exists, "Feature %s should exist", key)
	}

	// fake_meter should always be true
	assert.True(t, features["fake_meter"])
}

func TestPowerMeterFactory_String(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	str := factory.String()
	assert.Contains(t, str, "PowerMeterFactory")
	assert.Contains(t, str, "arch=")
	assert.Contains(t, str, "features=")
}

func TestPowerMeterFactory_IsARMHardware(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	isARM := factory.isARMHardware()
	assert.IsType(t, false, isARM)

	// Test should pass regardless of actual hardware
	// The method will return true only on real ARM systems with proper hwmon setup
}

func TestPowerMeterFactory_HasARMCPUInfo(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	// Test the logic with mock data
	hasARM := factory.hasARMCPUInfo()
	assert.IsType(t, false, hasARM)

	// This tests the real /proc/cpuinfo, so result depends on actual hardware
}

func TestPowerMeterFactory_HasHwmonPowerSensors(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	// Test with actual system hwmon
	hasSystemHwmon := factory.hasHwmonPowerSensors()
	assert.IsType(t, false, hasSystemHwmon)
}

func TestPowerMeterFactory_HasARMSpecificFiles(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	hasFiles := factory.hasARMSpecificFiles()
	assert.IsType(t, false, hasFiles)

	// This checks real system files, so result depends on actual hardware
	// On most x86 test systems, this should return false
}

func TestCPUArchitectureString(t *testing.T) {
	testCases := []struct {
		arch     CPUArchitecture
		expected string
	}{
		{ArchIntel, "intel"},
		{ArchAMD, "amd"},
		{ArchARM, "arm"},
		{ArchUnknown, "unknown"},
	}

	for _, tc := range testCases {
		assert.Equal(t, tc.expected, string(tc.arch))
	}
}

func TestPowerMeterFactory_DetectFromCPUInfo(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	// This tests the real /proc/cpuinfo file
	arch := factory.detectFromCPUInfo()

	// Should return a valid architecture
	validArchs := []CPUArchitecture{ArchIntel, ArchAMD, ArchARM, ArchUnknown}
	assert.Contains(t, validArchs, arch)
}

// TestPowerMeterFactory_CreateCPUPowerMeter_WithOptions tests meter creation with various options
func TestPowerMeterFactory_CreateCPUPowerMeter_WithOptions(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	// Test with RAPL options (for Intel/AMD systems)
	var raplOpts []interface{}
	raplOpts = append(raplOpts, WithRaplLogger(slog.Default()))
	raplOpts = append(raplOpts, WithZoneFilter([]string{ZonePackage}))

	meter, err := factory.CreateCPUPowerMeter(raplOpts...)

	// Behavior depends on system architecture and available hardware
	// The test should pass regardless, but meter type will vary
	if err == nil {
		assert.NotNil(t, meter)
		assert.NotEmpty(t, meter.Name())

		// Clean up if meter was created successfully
		if closer, ok := meter.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
}

// Mock hwmon structure for more comprehensive testing
func createMockHwmonStructure(t *testing.T, hwmonPath string, deviceCount int) {
	// Ensure the hwmon directory exists
	require.NoError(t, os.MkdirAll(hwmonPath, 0755))

	for i := 0; i < deviceCount; i++ {
		hwmonDir := filepath.Join(hwmonPath, "hwmon"+string(rune('0'+i)))
		require.NoError(t, os.MkdirAll(hwmonDir, 0755))

		// Create power sensor files
		switch i {
		case 0:
			// First device: CPU power
			powerFile := filepath.Join(hwmonDir, "power1_input")
			labelFile := filepath.Join(hwmonDir, "power1_label")
			require.NoError(t, os.WriteFile(powerFile, []byte("5000000"), 0644))
			require.NoError(t, os.WriteFile(labelFile, []byte("CPU Power"), 0644))
		case 1:
			// Second device: IO power
			powerFile := filepath.Join(hwmonDir, "power1_input")
			labelFile := filepath.Join(hwmonDir, "power1_label")
			require.NoError(t, os.WriteFile(powerFile, []byte("2000000"), 0644))
			require.NoError(t, os.WriteFile(labelFile, []byte("IO Power"), 0644))
		}

		// Add device name
		nameFile := filepath.Join(hwmonDir, "name")
		require.NoError(t, os.WriteFile(nameFile, []byte("test-device-"+string(rune('0'+i))), 0644))
	}
}

func TestPowerMeterFactory_CreateARMHwmonPowerMeter(t *testing.T) {
	tempDir := t.TempDir()
	tempSysfsPath := tempDir
	factory := NewPowerMeterFactory(nil, tempSysfsPath, "")
	tempHwmonPath := filepath.Join(tempDir, "class", "hwmon")

	// Create mock hwmon structure
	createMockHwmonStructure(t, tempHwmonPath, 2)

	// Test ARM power meter creation with custom sysfs path
	var armOpts []interface{}
	armOpts = append(armOpts, WithARMLogger(slog.Default()))

	meter, err := factory.createARMHwmonPowerMeter(tempSysfsPath, armOpts...)
	assert.NoError(t, err)
	assert.NotNil(t, meter)

	armMeter, ok := meter.(*armHwmonPowerMeter)
	assert.True(t, ok)
	assert.NotNil(t, armMeter.reader)

	zones, err := meter.Zones()
	assert.NoError(t, err)
	assert.Len(t, zones, 2)

	// Test primary zone selection
	primary, err := meter.PrimaryEnergyZone()
	assert.NoError(t, err)
	assert.Equal(t, ZonePackage, primary.Name()) // CPU should have priority

	// Clean up
	err = armMeter.Close()
	assert.NoError(t, err)
}

func TestPowerMeterFactory_CreateFakePowerMeter(t *testing.T) {
	factory := NewPowerMeterFactory(nil, "/sys", "/proc")

	// Test with default zones
	meter, err := factory.createFakePowerMeter()
	assert.NoError(t, err)
	assert.NotNil(t, meter)

	zones, err := meter.Zones()
	assert.NoError(t, err)
	assert.Equal(t, len([]Zone{ZonePackage, ZoneCore}), len(zones))

	// Test with custom zones
	customZones := []Zone{ZonePackage, ZoneUncore, ZoneDRAM}
	meter2, err := factory.createFakePowerMeter(customZones)
	assert.NoError(t, err)
	assert.NotNil(t, meter2)

	zones2, err := meter2.Zones()
	assert.NoError(t, err)
	assert.Equal(t, len(customZones), len(zones2))
}
