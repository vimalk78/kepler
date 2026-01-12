// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package nvidia

import (
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/sync/singleflight"

	"github.com/sustainable-computing-io/kepler/internal/device"
	"github.com/sustainable-computing-io/kepler/internal/device/gpu"
)

// GPUPowerCollector implements gpu.GPUPowerMeter with hybrid logic for
// accurate per-process power attribution across different GPU sharing modes.
//
// # Hybrid Logic Overview
//
// The collector automatically detects the GPU sharing mode and selects the
// appropriate power attribution strategy:
//
// ## Scenario A: MIG Mode (Multi-Instance GPU)
//
// Constraint: Standard NVML utilization queries return N/A in MIG mode.
// Strategy: Use DCGM profiling with Field 1001 (DCGM_FI_PROF_GR_ENGINE_ACTIVE).
// Calculation:
//   - Get MIG instance's share of total board power (based on slice ratio)
//   - Attribute power to processes based on their activity within the MIG instance
//   - Formula: P_proc = (P_board * slice_ratio) * (Activity_proc / Sum_Activity_instance)
//
// ## Scenario B: Time-Slicing Mode
//
// Constraint: DCGM profiling often reports 100% utilization per process due to
// hardware counter measurement during time slices (not normalized capacity).
// Strategy: Use NVML nvmlDeviceGetProcessUtilization() which correctly accounts
// for time-shared context switching at the driver level.
// Calculation:
//   - Formula: P_proc = P_board * (SmUtil_proc / Sum_SmUtil_all)
//
// ## Scenario C: Exclusive Mode (Single Process)
//
// Strategy: Same as time-slicing but with 100% attribution to the single PID.
// Calculation: P_proc = P_board
type GPUPowerCollector struct {
	logger *slog.Logger

	// Backend handles
	nvml NVMLBackend // Always initialized if GPUs present
	dcgm DCGMBackend // Only initialized if MIG detected

	// Detection
	detector     SharingModeDetector
	sharingModes map[int]gpu.SharingMode // deviceIndex -> mode

	// Device info
	devices []gpu.GPUDevice

	// MIG configuration (populated once at Init from NVML - this is static)
	totalGPUSlices       uint                       // Max MIG slices per GPU (7 for A100, 8 for H100)
	migInstancesByDevice map[int][]MIGGPUInstance   // deviceIndex -> MIG instances (from NVML)

	// State management
	mu          sync.RWMutex
	initialized bool

	// Idle power tracking per device (for active power calculation)
	idlePower        map[int]float64 // Configured idle power (optional override)
	minObservedPower map[int]float64 // Auto-detected minimum observed power

	// Singleflight to coalesce concurrent GetProcessPower calls.
	// Prometheus scrapes can overlap - this ensures only one NVML collection
	// runs at a time, preventing contention and gaps in metrics.
	processPowerGroup singleflight.Group
}

// CollectorOption configures the GPUPowerCollector
type CollectorOption func(*GPUPowerCollector)

// WithLogger sets the logger for the collector
func WithLogger(logger *slog.Logger) CollectorOption {
	return func(c *GPUPowerCollector) {
		c.logger = logger
	}
}

// WithNVMLBackend sets a custom NVML backend (useful for testing)
func WithNVMLBackend(nvml NVMLBackend) CollectorOption {
	return func(c *GPUPowerCollector) {
		c.nvml = nvml
	}
}

// WithDCGMBackend sets a custom DCGM backend (useful for testing)
func WithDCGMBackend(dcgm DCGMBackend) CollectorOption {
	return func(c *GPUPowerCollector) {
		c.dcgm = dcgm
	}
}

// NewGPUPowerCollector creates a new NVIDIA GPU power collector with hybrid logic
func NewGPUPowerCollector(opts ...CollectorOption) *GPUPowerCollector {
	c := &GPUPowerCollector{
		logger:           slog.Default(),
		sharingModes:     make(map[int]gpu.SharingMode),
		idlePower:        make(map[int]float64),
		minObservedPower: make(map[int]float64),
	}

	for _, opt := range opts {
		opt(c)
	}

	c.logger = c.logger.With("component", "nvidia-gpu-collector")

	// Create default backends if not provided
	if c.nvml == nil {
		c.nvml = NewNVMLBackend(c.logger)
	}
	if c.dcgm == nil {
		// Use dcgm-exporter HTTP backend instead of go-dcgm library
		// This queries dcgm-exporter's Prometheus endpoint, avoiding libdcgm.so dependency
		c.dcgm = NewDCGMExporterBackend(c.logger)
	}

	return c
}

// Name returns the service name
func (c *GPUPowerCollector) Name() string {
	return "nvidia-gpu-power-collector"
}

// Init initializes the collector with the following steps:
//  1. Initialize NVML (fails if NVML unavailable)
//  2. Discover GPU devices (fails if no GPUs found)
//  3. Detect sharing mode for each device (auto-detection for MIG/time-slicing/exclusive)
//  4. Initialize DCGM only if MIG mode is detected (to minimize overhead)
//
// If NVML initialization fails, an error is returned. The GPU collector should
// only be created when GPU support is explicitly configured.
func (c *GPUPowerCollector) Init() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.initialized {
		return nil
	}

	// Step 1: Initialize NVML
	if err := c.nvml.Init(); err != nil {
		return fmt.Errorf("NVML initialization failed: %w", err)
	}

	// Step 2: Discover devices
	devices, err := c.nvml.DiscoverDevices()
	if err != nil {
		_ = c.nvml.Shutdown()
		return fmt.Errorf("GPU discovery failed: %w", err)
	}
	c.devices = devices

	if len(c.devices) == 0 {
		_ = c.nvml.Shutdown()
		return fmt.Errorf("no NVIDIA GPUs found")
	}

	// Step 3: Create detector and detect sharing modes
	c.detector = NewSharingModeDetector(c.logger, c.nvml)
	hasMIG := false

	for i := range c.devices {
		mode, err := c.detector.DetectMode(i)
		if err != nil {
			c.logger.Warn("failed to detect sharing mode, defaulting to exclusive",
				"device", i, "error", err)
			mode = gpu.SharingModeExclusive
		}
		c.sharingModes[i] = mode

		if mode == gpu.SharingModeMIG {
			hasMIG = true
		}

		c.logger.Info("detected GPU sharing mode",
			"device", i,
			"name", c.devices[i].Name,
			"mode", mode.String())
	}

	// Step 4: Initialize DCGM only if MIG is detected
	// This is an optimization - DCGM adds overhead and is only needed for MIG
	if hasMIG {
		if err := c.dcgm.Init(); err != nil {
			c.logger.Warn("DCGM init failed, MIG power attribution will use fallback",
				"error", err)
			// Continue without DCGM - we'll fall back to device-level power
		}
		// Step 5: Cache MIG hierarchy from NVML (static topology, not from dcgm-exporter)
		if err := c.cacheMIGHierarchy(); err != nil {
			c.logger.Warn("failed to cache MIG hierarchy", "error", err)
		}
	}

	c.initialized = true
	c.logger.Info("GPU power collector initialized",
		"device_count", len(c.devices),
		"mig_enabled", hasMIG)
	return nil
}

// cacheMIGHierarchy enumerates all MIG instances from NVML at startup.
// This is static data - MIG topology doesn't change without admin action.
// dcgm-exporter is only used for the activity metric during collection.
func (c *GPUPowerCollector) cacheMIGHierarchy() error {
	c.migInstancesByDevice = make(map[int][]MIGGPUInstance)
	var totalInstances int

	for deviceIndex, mode := range c.sharingModes {
		if mode != gpu.SharingModeMIG {
			continue
		}

		dev, err := c.nvml.GetDevice(deviceIndex)
		if err != nil {
			c.logger.Warn("failed to get device for MIG enumeration",
				"device", deviceIndex, "error", err)
			continue
		}

		// Get totalGPUSlices once (same for all devices of same model)
		if c.totalGPUSlices == 0 {
			maxMigCount, err := dev.GetMaxMigDeviceCount()
			if err != nil || maxMigCount == 0 {
				c.logger.Warn("failed to get max MIG device count, using default 7", "error", err)
				c.totalGPUSlices = 7
			} else {
				c.totalGPUSlices = uint(maxMigCount)
			}
		}

		// Enumerate MIG instances from NVML (this finds ALL instances, not just active ones)
		nvmlInstances, err := dev.GetMIGInstances()
		if err != nil {
			c.logger.Warn("failed to enumerate MIG instances",
				"device", deviceIndex, "error", err)
			continue
		}

		// Convert NVML MIGInstance to our MIGGPUInstance
		for _, inst := range nvmlInstances {
			c.migInstancesByDevice[deviceIndex] = append(c.migInstancesByDevice[deviceIndex],
				MIGGPUInstance{
					ParentGPUIndex:     deviceIndex,
					GPUInstanceID:      inst.GPUInstanceID,
					EntityID:           inst.EntityID,
					ProfileSlices:      inst.ProfileSlices,
					TotalGPUSlices:     c.totalGPUSlices,
					ComputeInstanceIDs: []uint{},
				})
			totalInstances++
		}

		c.logger.Debug("enumerated MIG instances for device",
			"device", deviceIndex, "instances", len(nvmlInstances))
	}

	c.logger.Info("cached MIG hierarchy from NVML",
		"totalGPUSlices", c.totalGPUSlices,
		"totalInstances", totalInstances)
	return nil
}

// Shutdown cleans up all resources
func (c *GPUPowerCollector) Shutdown() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.initialized {
		return nil
	}

	var errs []error

	if c.dcgm != nil && c.dcgm.IsInitialized() {
		if err := c.dcgm.Shutdown(); err != nil {
			errs = append(errs, fmt.Errorf("DCGM shutdown: %w", err))
		}
	}

	if c.nvml != nil {
		if err := c.nvml.Shutdown(); err != nil {
			errs = append(errs, fmt.Errorf("NVML shutdown: %w", err))
		}
	}

	c.initialized = false
	c.logger.Info("GPU power collector shutdown complete")

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	return nil
}

// Vendor returns the GPU vendor
func (c *GPUPowerCollector) Vendor() gpu.Vendor {
	return gpu.VendorNVIDIA
}

// Devices returns all discovered GPU devices
func (c *GPUPowerCollector) Devices() []gpu.GPUDevice {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make([]gpu.GPUDevice, len(c.devices))
	copy(result, c.devices)
	return result
}

// GetPowerUsage returns the current power consumption for a device in Watts
func (c *GPUPowerCollector) GetPowerUsage(deviceIndex int) (device.Power, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.initialized {
		return 0, gpu.ErrGPUNotInitialized{}
	}

	dev, err := c.nvml.GetDevice(deviceIndex)
	if err != nil {
		return 0, err
	}

	return dev.GetPowerUsage()
}

// GetTotalEnergy returns cumulative energy consumption for a device in Joules
func (c *GPUPowerCollector) GetTotalEnergy(deviceIndex int) (device.Energy, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.initialized {
		return 0, gpu.ErrGPUNotInitialized{}
	}

	dev, err := c.nvml.GetDevice(deviceIndex)
	if err != nil {
		return 0, err
	}

	return dev.GetTotalEnergy()
}

// GetDevicePowerStats returns power statistics for a device including idle power detection
func (c *GPUPowerCollector) GetDevicePowerStats(deviceIndex int) (gpu.GPUPowerStats, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stats := gpu.GPUPowerStats{}

	if !c.initialized {
		return stats, gpu.ErrGPUNotInitialized{}
	}

	dev, err := c.nvml.GetDevice(deviceIndex)
	if err != nil {
		return stats, err
	}

	totalPower, err := dev.GetPowerUsage()
	if err != nil {
		return stats, err
	}

	stats.TotalPower = totalPower.Watts()

	// Get idle power (configured or auto-detected minimum)
	idlePower := c.idlePower[deviceIndex]
	if idlePower == 0 {
		idlePower = c.minObservedPower[deviceIndex]
	}
	stats.IdlePower = idlePower

	stats.ActivePower = stats.TotalPower - stats.IdlePower
	if stats.ActivePower < 0 {
		stats.ActivePower = 0
	}

	return stats, nil
}

// GetProcessPower returns power attribution per process using hybrid logic.
// The map key is PID and value is power in Watts.
//
// This is the core method implementing the hybrid strategy:
//   - MIG mode: DCGM with activity-based attribution
//   - Time-slicing: NVML GetProcessUtilization() with proportional attribution
//   - Exclusive: 100% to single process
// processPowerResult wraps the result for singleflight (which only returns interface{})
type processPowerResult struct {
	power map[uint32]float64
	err   error
}

// GetProcessPower returns power consumption per process.
// Uses singleflight to coalesce concurrent Prometheus scrape calls - only one
// NVML collection runs at a time, preventing contention and gaps in metrics.
func (c *GPUPowerCollector) GetProcessPower() (map[uint32]float64, error) {
	// Use singleflight to prevent concurrent NVML collections
	result, _, _ := c.processPowerGroup.Do("process-power", func() (interface{}, error) {
		return c.collectProcessPower(), nil
	})

	r := result.(processPowerResult)
	return r.power, r.err
}

// collectProcessPower is the internal implementation called via singleflight.
func (c *GPUPowerCollector) collectProcessPower() processPowerResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.initialized {
		c.logger.Debug("GetProcessPower called but not initialized")
		return processPowerResult{nil, gpu.ErrGPUNotInitialized{}}
	}

	c.logger.Debug("GetProcessPower called", "devices", len(c.sharingModes))

	result := make(map[uint32]float64)

	for deviceIndex, mode := range c.sharingModes {
		var deviceResult map[uint32]float64
		var err error

		switch mode {
		case gpu.SharingModeMIG:
			deviceResult, err = c.attributeMIGPower(deviceIndex)
		case gpu.SharingModeTimeSlicing:
			deviceResult, err = c.attributeTimeSlicingPower(deviceIndex)
		case gpu.SharingModeExclusive:
			deviceResult, err = c.attributeExclusivePower(deviceIndex)
		default:
			c.logger.Warn("unknown sharing mode, using exclusive fallback",
				"device", deviceIndex, "mode", mode)
			deviceResult, err = c.attributeExclusivePower(deviceIndex)
		}

		if err != nil {
			c.logger.Warn("power attribution failed",
				"device", deviceIndex,
				"mode", mode.String(),
				"error", err)
			continue
		}

		// Merge results (aggregate if PID uses multiple GPUs)
		for pid, power := range deviceResult {
			result[pid] += power
		}
		c.logger.Debug("device attribution result", "device", deviceIndex, "mode", mode.String(), "processes", len(deviceResult))
	}

	c.logger.Debug("GetProcessPower result", "total_processes", len(result))
	return processPowerResult{result, nil}
}

// GetProcessInfo returns detailed GPU metrics per process.
func (c *GPUPowerCollector) GetProcessInfo() ([]gpu.ProcessGPUInfo, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.initialized {
		return nil, gpu.ErrGPUNotInitialized{}
	}

	var allProcesses []gpu.ProcessGPUInfo

	for i := 0; i < len(c.devices); i++ {
		dev, err := c.nvml.GetDevice(i)
		if err != nil {
			continue
		}

		processes, err := dev.GetComputeRunningProcesses()
		if err != nil {
			continue
		}

		allProcesses = append(allProcesses, processes...)
	}

	return allProcesses, nil
}

// attributeMIGPower handles power attribution for MIG mode using DCGM.
//
// When MIG is enabled, standard NVML utilization queries return N/A because
// the physical GPU is partitioned. DCGM's profiling metrics (Field 1001/1002)
// query activity at the GPU Instance level.
//
// Two independent metrics are used at different levels:
//   - activity (DCGM): Time ratio compute engine was active (0.0-1.0)
//   - SmUtil (NVML): Per-process SM usage percentage (0-100)
//
// These metrics don't match and aren't expected to - activity measures "was
// the instance doing anything" while SmUtil measures "what % of SMs did each
// process use". In our code, they serve different purposes:
//  1. activity → determines MIG instance's share of total GPU power
//  2. SmUtil ratios → distributes that power among processes within the instance
//
// Hierarchical formula:
//
//	P_process = P_board × (activity_i / Σactivity) × (SmUtil_p / ΣSmUtil_in_instance)
func (c *GPUPowerCollector) attributeMIGPower(deviceIndex int) (map[uint32]float64, error) {
	dev, err := c.nvml.GetDevice(deviceIndex)
	if err != nil {
		return nil, err
	}

	totalPower, err := dev.GetPowerUsage()
	if err != nil {
		return nil, fmt.Errorf("failed to get board power: %w", err)
	}
	totalPowerWatts := totalPower.Watts()

	// Track minimum observed power for idle detection
	if minPower, ok := c.minObservedPower[deviceIndex]; !ok || totalPowerWatts < minPower {
		c.minObservedPower[deviceIndex] = totalPowerWatts
	}

	// Use cached MIG hierarchy from NVML (static, enumerated at startup)
	instances := c.migInstancesByDevice[deviceIndex]
	if !c.dcgm.IsInitialized() || len(instances) == 0 {
		c.logger.Debug("DCGM not available or no MIG instances, using fallback",
			"dcgm_initialized", c.dcgm.IsInitialized(),
			"instances", len(instances))
		return c.fallbackAttribution(deviceIndex, totalPower)
	}

	// Collect activity and process data for each MIG instance
	type instanceData struct {
		activity    float64
		processUtil map[uint32]uint32 // PID -> SmUtil (0 if unavailable)
	}
	data := make([]instanceData, 0, len(instances))
	var totalActivity float64

	for _, gi := range instances {
		activity, err := c.dcgm.GetMIGInstanceActivity(deviceIndex, gi.GPUInstanceID)
		if err != nil {
			continue
		}

		// Skip NVML calls for idle MIG instances (major optimization)
		// Activity from dcgm-exporter is cached and cheap to query.
		// NVML calls (GetMIGDeviceByInstanceID, GetProcessUtilization) are slow (~50-100ms each).
		if activity == 0 {
			continue
		}

		migDevice, err := dev.GetMIGDeviceByInstanceID(gi.GPUInstanceID)
		if err != nil {
			continue
		}

		// Try GetProcessUtilization first (includes PIDs + utilization)
		processUtil := make(map[uint32]uint32)
		if utils, err := migDevice.GetProcessUtilization(0); err == nil && len(utils) > 0 {
			for _, u := range utils {
				processUtil[u.PID] = u.SmUtil
			}
		} else {
			// Fallback: get PIDs from GetComputeRunningProcesses (equal distribution)
			procs, err := migDevice.GetComputeRunningProcesses()
			if err != nil || len(procs) == 0 {
				continue
			}
			for _, p := range procs {
				processUtil[p.PID] = 0
			}
		}

		if len(processUtil) == 0 {
			continue
		}

		data = append(data, instanceData{activity: activity, processUtil: processUtil})
		totalActivity += activity
	}

	if len(data) == 0 || totalActivity == 0 {
		return make(map[uint32]float64), nil
	}

	// Calculate active power (total - idle)
	idlePower := c.idlePower[deviceIndex]
	if idlePower == 0 {
		idlePower = c.minObservedPower[deviceIndex]
	}
	activePower := totalPowerWatts - idlePower
	if activePower < 0 {
		activePower = 0
	}

	// Distribute active power based on activity
	result := make(map[uint32]float64)
	for _, d := range data {
		migPower := activePower * (d.activity / totalActivity)
		distributeByUtilization(result, d.processUtil, migPower)
	}

	return result, nil
}

// distributeByUtilization distributes power among processes based on utilization.
// Falls back to equal distribution if all utilization values are zero.
func distributeByUtilization(result map[uint32]float64, processUtil map[uint32]uint32, power float64) {
	var totalUtil uint32
	for _, util := range processUtil {
		totalUtil += util
	}

	if totalUtil > 0 {
		for pid, util := range processUtil {
			result[pid] += power * float64(util) / float64(totalUtil)
		}
	} else {
		powerPerProcess := power / float64(len(processUtil))
		for pid := range processUtil {
			result[pid] += powerPerProcess
		}
	}
}

// attributeTimeSlicingPower handles power attribution for time-slicing mode using NVML.
//
// Why NVML for time-slicing:
// DCGM profiling (Field 1001) measures hardware counter residency during each
// process's time slice, often reporting 100% utilization for each process.
// NVML's GetProcessUtilization() correctly accounts for time-shared context
// switching at the driver level, providing normalized utilization.
//
// Formula: P_process = P_board * (SmUtil_process / Sum(SmUtil_all_processes))
func (c *GPUPowerCollector) attributeTimeSlicingPower(deviceIndex int) (map[uint32]float64, error) {
	result := make(map[uint32]float64)

	dev, err := c.nvml.GetDevice(deviceIndex)
	if err != nil {
		return nil, err
	}

	// Get total board power
	totalPower, err := dev.GetPowerUsage()
	if err != nil {
		return nil, fmt.Errorf("failed to get board power: %w", err)
	}
	totalPowerWatts := totalPower.Watts()

	// Track minimum observed power for idle detection
	if minPower, ok := c.minObservedPower[deviceIndex]; !ok || totalPowerWatts < minPower {
		c.minObservedPower[deviceIndex] = totalPowerWatts
	}

	// Calculate active power (total - idle)
	// Use configured idle power if set, otherwise use auto-detected minimum
	idlePower := c.idlePower[deviceIndex]
	if idlePower == 0 {
		idlePower = c.minObservedPower[deviceIndex]
	}
	activePower := totalPowerWatts - idlePower
	if activePower < 0 {
		activePower = 0
	}

	// Step 1: Get list of running processes (authoritative list)
	runningProcs, err := dev.GetComputeRunningProcesses()
	if err != nil {
		c.logger.Debug("GetComputeRunningProcesses failed, using fallback",
			"device", deviceIndex, "error", err)
		return c.fallbackAttribution(deviceIndex, totalPower)
	}

	if len(runningProcs) == 0 {
		return result, nil
	}

	// Step 2: Get utilization samples (may not have all processes)
	processUtils, err := dev.GetProcessUtilization(0)
	if err != nil {
		c.logger.Debug("process utilization unavailable, using fallback",
			"device", deviceIndex, "error", err)
		return c.fallbackAttribution(deviceIndex, totalPower)
	}

	// Step 3: Build utilization map by PID
	utilMap := make(map[uint32]uint32) // PID -> SmUtil
	for _, pu := range processUtils {
		// Keep the highest utilization for each PID (samples may have duplicates)
		if existing, ok := utilMap[pu.PID]; !ok || pu.SmUtil > existing {
			utilMap[pu.PID] = pu.SmUtil
		}
	}

	c.logger.Debug("GetProcessUtilization result",
		"device", deviceIndex,
		"runningProcs", len(runningProcs),
		"utilSamples", len(processUtils),
		"utilMapSize", len(utilMap),
		"totalPower", totalPowerWatts,
		"idlePower", idlePower,
		"activePower", activePower)

	// Step 4: Calculate total SM utilization across running processes
	var totalSmUtil uint32
	for _, proc := range runningProcs {
		if smUtil, ok := utilMap[proc.PID]; ok {
			totalSmUtil += smUtil
		}
	}

	// If no utilization data, distribute equally among running processes
	if totalSmUtil == 0 {
		powerPerProcess := activePower / float64(len(runningProcs))
		for _, proc := range runningProcs {
			result[proc.PID] = powerPerProcess
		}
		c.logger.Debug("no utilization data, using equal distribution",
			"device", deviceIndex,
			"processes", len(runningProcs),
			"powerPerProcess", powerPerProcess)
		return result, nil
	}

	// Step 5: Attribute ACTIVE power proportionally to SM utilization
	// Formula: P_process = P_active * (SmUtil_process / Sum(SmUtil_all))
	for _, proc := range runningProcs {
		smUtil := utilMap[proc.PID] // 0 if not in map
		ratio := float64(smUtil) / float64(totalSmUtil)
		result[proc.PID] = activePower * ratio
		c.logger.Debug("process utilization attribution",
			"device", deviceIndex,
			"pid", proc.PID,
			"smUtil", smUtil,
			"totalSmUtil", totalSmUtil,
			"ratio", ratio,
			"power", result[proc.PID])
	}

	return result, nil
}

// attributeExclusivePower handles power attribution for exclusive mode.
// In this mode, a single process has exclusive access to the GPU,
// so it gets 100% of the board power.
//
// TODO: Consider merging with attributeTimeSlicingPower - exclusive mode is just
// a driver-level restriction, but for power attribution the logic is identical.
func (c *GPUPowerCollector) attributeExclusivePower(deviceIndex int) (map[uint32]float64, error) {
	result := make(map[uint32]float64)

	dev, err := c.nvml.GetDevice(deviceIndex)
	if err != nil {
		return nil, err
	}

	// Get running processes
	processes, err := dev.GetComputeRunningProcesses()
	if err != nil {
		return nil, fmt.Errorf("failed to get running processes: %w", err)
	}

	if len(processes) == 0 {
		// No active processes
		return result, nil
	}

	// Get total board power
	totalPower, err := dev.GetPowerUsage()
	if err != nil {
		return nil, fmt.Errorf("failed to get board power: %w", err)
	}

	// 100% attribution to the single process (or first process if multiple detected)
	result[processes[0].PID] = totalPower.Watts()

	if len(processes) > 1 {
		c.logger.Warn("multiple processes detected in exclusive mode, only first gets power",
			"device", deviceIndex, "processes", len(processes))
	}

	return result, nil
}

// fallbackAttribution distributes power equally among all running processes.
// Used when more accurate attribution methods are unavailable.
func (c *GPUPowerCollector) fallbackAttribution(deviceIndex int, totalPower device.Power) (map[uint32]float64, error) {
	result := make(map[uint32]float64)

	dev, err := c.nvml.GetDevice(deviceIndex)
	if err != nil {
		return nil, err
	}

	totalPowerWatts := totalPower.Watts()

	// Track minimum observed power for idle detection
	if minPower, ok := c.minObservedPower[deviceIndex]; !ok || totalPowerWatts < minPower {
		c.minObservedPower[deviceIndex] = totalPowerWatts
	}

	// Calculate active power (total - idle)
	idlePower := c.idlePower[deviceIndex]
	if idlePower == 0 {
		idlePower = c.minObservedPower[deviceIndex]
	}
	activePower := totalPowerWatts - idlePower
	if activePower < 0 {
		activePower = 0
	}

	processes, err := dev.GetComputeRunningProcesses()
	if err != nil {
		c.logger.Debug("GetComputeRunningProcesses failed", "device", deviceIndex, "error", err)
		return nil, fmt.Errorf("failed to get running processes: %w", err)
	}

	c.logger.Debug("GetComputeRunningProcesses result", "device", deviceIndex, "process_count", len(processes))

	if len(processes) == 0 {
		return result, nil
	}

	// Equal distribution of ACTIVE power among all processes
	powerPerProcess := activePower / float64(len(processes))
	for _, p := range processes {
		result[p.PID] = powerPerProcess
	}

	c.logger.Debug("using equal power distribution fallback",
		"device", deviceIndex,
		"processes", len(processes),
		"totalPower", totalPowerWatts,
		"idlePower", idlePower,
		"activePower", activePower,
		"power_per_process", powerPerProcess)

	return result, nil
}

// RefreshModes re-detects the sharing mode for all GPUs.
// Call this when process topology changes significantly.
func (c *GPUPowerCollector) RefreshModes() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.initialized || c.detector == nil {
		return nil
	}

	modes, err := c.detector.DetectAllModes()
	if err != nil {
		return err
	}

	c.sharingModes = modes
	return nil
}
