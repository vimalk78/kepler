// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package device

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
)

// CPUArchitecture represents the CPU architecture type
type CPUArchitecture string

const (
	ArchIntel   CPUArchitecture = "intel"
	ArchAMD     CPUArchitecture = "amd"
	ArchARM     CPUArchitecture = "arm"
	ArchUnknown CPUArchitecture = "unknown"
)

// PowerMeterFactory creates CPU power meters based on system architecture
type PowerMeterFactory struct {
	logger     *slog.Logger
	sysfsPath  string
	procfsPath string
}

// NewPowerMeterFactory creates a new power meter factory
func NewPowerMeterFactory(logger *slog.Logger, sysfsPath, procfsPath string) *PowerMeterFactory {
	if logger == nil {
		logger = slog.Default()
	}
	return &PowerMeterFactory{
		logger:     logger.With("component", "power-meter-factory"),
		sysfsPath:  sysfsPath,
		procfsPath: procfsPath,
	}
}

// CreateCPUPowerMeter creates an appropriate CPU power meter for the current system
func (f *PowerMeterFactory) CreateCPUPowerMeter(opts ...interface{}) (CPUPowerMeter, error) {
	arch := f.DetectArchitecture()
	f.logger.Info("Detected system architecture", "architecture", arch, "runtime_arch", runtime.GOARCH)

	switch arch {
	case ArchIntel, ArchAMD:
		return f.createIntelRAPLPowerMeter(f.sysfsPath, opts...)
	case ArchARM:
		return f.createARMHwmonPowerMeter(f.sysfsPath, opts...)
	default:
		f.logger.Warn("Unknown architecture detected, falling back to fake power meter", "architecture", arch)
		return f.createFakePowerMeter(opts...)
	}
}

// DetectArchitecture detects the CPU architecture
func (f *PowerMeterFactory) DetectArchitecture() CPUArchitecture {
	// First check runtime architecture
	switch runtime.GOARCH {
	case "arm64", "arm":
		// For ARM, verify we're on real hardware, not emulation
		if f.isARMHardware() {
			return ArchARM
		}
	case "amd64", "386":
		// For x86_64, check if it's Intel or AMD
		return f.detectX86Vendor()
	}

	// Fallback: check /proc/cpuinfo if available
	if arch := f.detectFromCPUInfo(); arch != ArchUnknown {
		return arch
	}

	f.logger.Debug("Could not detect specific architecture", "runtime_arch", runtime.GOARCH)
	return ArchUnknown
}

// isARMHardware checks if we're running on real ARM hardware
func (f *PowerMeterFactory) isARMHardware() bool {
	return f.hasARMCPUInfo() || f.hasARMSpecificFiles()
}

// hasARMCPUInfo checks /proc/cpuinfo for ARM indicators
func (f *PowerMeterFactory) hasARMCPUInfo() bool {
	cpuinfoPath := fmt.Sprintf("%s/cpuinfo", f.procfsPath)
	data, err := os.ReadFile(cpuinfoPath)
	if err != nil {
		return false
	}

	content := strings.ToLower(string(data))
	armIndicators := []string{
		"cpu implementer",  // ARM-specific field
		"cpu architecture", // ARM-specific field
		"cpu variant",      // ARM-specific field
		"features",         // ARM uses "Features" vs Intel's "flags"
		"bogomips",         // Common in ARM systems
	}

	for _, indicator := range armIndicators {
		if strings.Contains(content, indicator) {
			f.logger.Debug("Found ARM indicator in cpuinfo", "indicator", indicator)
			return true
		}
	}

	return false
}

// hasHwmonPowerSensors checks for hwmon power sensors (ARM systems)
func (f *PowerMeterFactory) hasHwmonPowerSensors() bool {
	hwmonPath := fmt.Sprintf("%s/class/hwmon", f.sysfsPath)
	entries, err := os.ReadDir(hwmonPath)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "hwmon") {
			continue
		}

		devicePath := fmt.Sprintf("%s/%s", hwmonPath, entry.Name())
		deviceEntries, err := os.ReadDir(devicePath)
		if err != nil {
			continue
		}

		// Look for power sensors
		for _, deviceEntry := range deviceEntries {
			if strings.HasPrefix(deviceEntry.Name(), "power") && strings.HasSuffix(deviceEntry.Name(), "_input") {
				f.logger.Debug("Found hwmon power sensor", "path", fmt.Sprintf("%s/%s", devicePath, deviceEntry.Name()))
				return true
			}
		}
	}

	return false
}

// hasARMSpecificFiles checks for ARM-specific system files
func (f *PowerMeterFactory) hasARMSpecificFiles() bool {
	armFiles := []string{
		fmt.Sprintf("%s/devices/system/cpu/cpu0/topology/physical_package_id", f.sysfsPath), // ARM topology
		fmt.Sprintf("%s/device-tree", f.procfsPath),                                         // ARM device tree
		fmt.Sprintf("%s/firmware/devicetree", f.sysfsPath),                                  // ARM device tree (newer kernels)
	}

	for _, file := range armFiles {
		if _, err := os.Stat(file); err == nil {
			f.logger.Debug("Found ARM-specific file", "file", file)
			return true
		}
	}

	return false
}

// detectX86Vendor detects Intel vs AMD on x86_64 systems
func (f *PowerMeterFactory) detectX86Vendor() CPUArchitecture {
	cpuinfoPath := fmt.Sprintf("%s/cpuinfo", f.procfsPath)
	data, err := os.ReadFile(cpuinfoPath)
	if err != nil {
		f.logger.Debug("Could not read cpuinfo for x86 vendor detection", "path", cpuinfoPath)
		return ArchIntel // Default to Intel for x86_64
	}

	content := strings.ToLower(string(data))

	if strings.Contains(content, "genuineintel") {
		return ArchIntel
	}
	if strings.Contains(content, "authenticamd") {
		return ArchAMD
	}

	// Check model names
	if strings.Contains(content, "intel") {
		return ArchIntel
	}
	if strings.Contains(content, "amd") {
		return ArchAMD
	}

	// Default to Intel for x86_64
	return ArchIntel
}

// detectFromCPUInfo attempts to detect architecture from /proc/cpuinfo
func (f *PowerMeterFactory) detectFromCPUInfo() CPUArchitecture {
	cpuinfoPath := fmt.Sprintf("%s/cpuinfo", f.procfsPath)
	data, err := os.ReadFile(cpuinfoPath)
	if err != nil {
		return ArchUnknown
	}

	content := strings.ToLower(string(data))

	// ARM indicators
	armKeywords := []string{"arm", "aarch64", "cpu implementer", "bogomips"}
	for _, keyword := range armKeywords {
		if strings.Contains(content, keyword) {
			return ArchARM
		}
	}

	// x86 indicators
	if strings.Contains(content, "intel") {
		return ArchIntel
	}
	if strings.Contains(content, "amd") {
		return ArchAMD
	}

	return ArchUnknown
}

// createIntelRAPLPowerMeter creates an Intel/AMD RAPL power meter
func (f *PowerMeterFactory) createIntelRAPLPowerMeter(sysfsPath string, opts ...interface{}) (CPUPowerMeter, error) {
	f.logger.Debug("Creating Intel/AMD RAPL power meter", "sysfs_path", sysfsPath)

	// Extract RAPL-specific options
	var raplOpts []OptionFn
	for _, opt := range opts {
		if raplOpt, ok := opt.(OptionFn); ok {
			raplOpts = append(raplOpts, raplOpt)
		}
	}

	meter, err := NewCPUPowerMeter(sysfsPath, raplOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create RAPL power meter: %w", err)
	}

	// Initialize the meter
	if err := meter.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize RAPL power meter: %w", err)
	}

	return meter, nil
}

// createARMHwmonPowerMeter creates an ARM hwmon power meter
func (f *PowerMeterFactory) createARMHwmonPowerMeter(sysfsPath string, opts ...interface{}) (CPUPowerMeter, error) {
	f.logger.Debug("Creating ARM hwmon power meter", "sysfs_path", sysfsPath)

	// Extract ARM-specific options
	var armOpts []ARMPowerMeterOption
	for _, opt := range opts {
		if armOpt, ok := opt.(ARMPowerMeterOption); ok {
			armOpts = append(armOpts, armOpt)
		}
	}

	// Always add logger
	armOpts = append(armOpts, WithARMLogger(f.logger))

	meter, err := NewARMHwmonPowerMeter(sysfsPath, armOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create ARM hwmon power meter: %w", err)
	}

	// Initialize the meter
	if err := meter.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize ARM hwmon power meter: %w", err)
	}

	return meter, nil
}

// createFakePowerMeter creates a fake power meter for unsupported systems
func (f *PowerMeterFactory) createFakePowerMeter(opts ...interface{}) (CPUPowerMeter, error) {
	f.logger.Debug("Creating fake power meter")

	// Default fake zones for compatibility
	fakeZones := []Zone{ZonePackage, ZoneCore}

	// Extract fake power meter options
	for _, opt := range opts {
		if zones, ok := opt.([]Zone); ok {
			fakeZones = zones
		}
	}

	return NewFakeCPUMeter(fakeZones, WithFakeLogger(f.logger))
}

// HasRAPLSupport checks if RAPL is available on the system
func (f *PowerMeterFactory) HasRAPLSupport() bool {
	raplPath := fmt.Sprintf("%s/class/powercap/intel-rapl", f.sysfsPath)
	if _, err := os.Stat(raplPath); err != nil {
		return false
	}

	// Check if there are actual RAPL zones
	entries, err := os.ReadDir(raplPath)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "intel-rapl:") {
			return true
		}
	}

	return false
}

// HasARMHwmonSupport checks if ARM hwmon power sensors are available
func (f *PowerMeterFactory) HasARMHwmonSupport() bool {
	// Must be ARM architecture AND have hwmon power sensors
	return f.isARMHardware() && f.hasHwmonPowerSensors()
}

// GetSupportedFeatures returns information about supported power monitoring features
func (f *PowerMeterFactory) GetSupportedFeatures() map[string]bool {
	return map[string]bool{
		"rapl":       f.HasRAPLSupport(),
		"arm_hwmon":  f.HasARMHwmonSupport(),
		"fake_meter": true, // Always available as fallback
	}
}

// String returns a string representation of the factory
func (f *PowerMeterFactory) String() string {
	arch := f.DetectArchitecture()
	features := f.GetSupportedFeatures()
	return fmt.Sprintf("PowerMeterFactory{arch=%s, features=%+v}", arch, features)
}
