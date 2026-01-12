// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package nvidia

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/sustainable-computing-io/kepler/internal/device"
	"github.com/sustainable-computing-io/kepler/internal/device/gpu"
)

// MIGInstance represents a Multi-Instance GPU partition (NVIDIA-specific)
type MIGInstance struct {
	EntityID         uint
	GPUInstanceID    uint
	ComputeInstances []ComputeInstance
	ProfileSlices    uint // Number of GPU slices allocated to this instance
}

// ComputeInstance represents a compute instance within a MIG GPU instance
type ComputeInstance struct {
	EntityID          uint
	ComputeInstanceID uint
}

// NVMLBackend provides access to NVIDIA GPUs via the NVML library.
// It is used for:
//   - Device discovery and power readings (all scenarios)
//   - Per-process utilization via GetProcessUtilization() (time-slicing scenario)
//   - MIG mode detection
//
// Thread-safety: All methods are safe for concurrent use.
type NVMLBackend interface {
	Init() error
	Shutdown() error
	DeviceCount() int
	GetDevice(index int) (NVMLDevice, error)
	DiscoverDevices() ([]gpu.GPUDevice, error)
}

// NVMLDevice wraps operations on a single NVIDIA GPU device
type NVMLDevice interface {
	Index() int
	UUID() string
	Name() string
	GetPowerUsage() (device.Power, error)
	GetTotalEnergy() (device.Energy, error)
	GetComputeRunningProcesses() ([]gpu.ProcessGPUInfo, error)
	GetProcessUtilization(lastSeen uint64) ([]gpu.ProcessUtilization, error)
	GetComputeMode() (gpu.ComputeMode, error)
	IsMIGEnabled() (bool, error)
	GetMIGInstances() ([]MIGInstance, error)
	GetMIGDeviceByInstanceID(gpuInstanceID uint) (NVMLDevice, error)
	GetMaxMigDeviceCount() (int, error)
}

// nvmlBackend is the concrete implementation of NVMLBackend
type nvmlBackend struct {
	logger      *slog.Logger
	devices     []nvmlDevice
	initialized bool
	mu          sync.RWMutex
}

// nvmlDevice wraps a single NVML device handle
type nvmlDevice struct {
	index  int
	handle nvml.Device
	uuid   string
	name   string
}

// NewNVMLBackend creates a new NVML backend instance
func NewNVMLBackend(logger *slog.Logger) NVMLBackend {
	if logger == nil {
		logger = slog.Default()
	}
	return &nvmlBackend{
		logger: logger.With("component", "nvml"),
	}
}

// Init initializes the NVML library and discovers all GPU devices
func (n *nvmlBackend) Init() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.initialized {
		return nil
	}

	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("NVML init failed: %s", nvml.ErrorString(ret))
	}

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		_ = nvml.Shutdown()
		return fmt.Errorf("failed to get device count: %s", nvml.ErrorString(ret))
	}

	n.devices = make([]nvmlDevice, 0, count)
	for i := 0; i < count; i++ {
		handle, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			n.logger.Warn("failed to get device handle", "index", i, "error", nvml.ErrorString(ret))
			continue
		}

		uuid, ret := handle.GetUUID()
		if ret != nvml.SUCCESS {
			uuid = fmt.Sprintf("gpu-%d", i)
		}

		name, ret := handle.GetName()
		if ret != nvml.SUCCESS {
			name = "Unknown NVIDIA GPU"
		}

		n.devices = append(n.devices, nvmlDevice{
			index:  i,
			handle: handle,
			uuid:   uuid,
			name:   name,
		})

		n.logger.Info("discovered GPU", "index", i, "uuid", uuid, "name", name)
	}

	n.initialized = true
	n.logger.Info("NVML initialized", "device_count", len(n.devices))
	return nil
}

// Shutdown cleans up NVML resources
func (n *nvmlBackend) Shutdown() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.initialized {
		return nil
	}

	ret := nvml.Shutdown()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("NVML shutdown failed: %s", nvml.ErrorString(ret))
	}

	n.devices = nil
	n.initialized = false
	n.logger.Info("NVML shutdown complete")
	return nil
}

// DeviceCount returns the number of discovered GPU devices
func (n *nvmlBackend) DeviceCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.devices)
}

// GetDevice returns an NVMLDevice for the given index
func (n *nvmlBackend) GetDevice(index int) (NVMLDevice, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if !n.initialized {
		return nil, gpu.ErrGPUNotInitialized{}
	}

	if index < 0 || index >= len(n.devices) {
		return nil, gpu.ErrGPUNotFound{DeviceIndex: index}
	}

	return &n.devices[index], nil
}

// DiscoverDevices returns GPU device information for all discovered devices
func (n *nvmlBackend) DiscoverDevices() ([]gpu.GPUDevice, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if !n.initialized {
		return nil, gpu.ErrGPUNotInitialized{}
	}

	devices := make([]gpu.GPUDevice, len(n.devices))
	for i, dev := range n.devices {
		devices[i] = gpu.GPUDevice{
			Index:  dev.index,
			UUID:   dev.uuid,
			Name:   dev.name,
			Vendor: gpu.VendorNVIDIA,
		}
	}

	return devices, nil
}

// Index returns the device index
func (d *nvmlDevice) Index() int {
	return d.index
}

// UUID returns the device UUID
func (d *nvmlDevice) UUID() string {
	return d.uuid
}

// Name returns the device name
func (d *nvmlDevice) Name() string {
	return d.name
}

// GetPowerUsage returns the current power consumption in Watts
func (d *nvmlDevice) GetPowerUsage() (device.Power, error) {
	// NVML returns power in milliwatts
	powerMW, ret := d.handle.GetPowerUsage()
	if ret != nvml.SUCCESS {
		return 0, fmt.Errorf("failed to get power usage: %s", nvml.ErrorString(ret))
	}

	// Convert milliwatts to device.Power (which is in microwatts)
	return device.Power(powerMW) * device.MilliWatt, nil
}

// GetTotalEnergy returns cumulative energy consumption in Joules
func (d *nvmlDevice) GetTotalEnergy() (device.Energy, error) {
	// NVML returns energy in millijoules
	energyMJ, ret := d.handle.GetTotalEnergyConsumption()
	if ret != nvml.SUCCESS {
		return 0, fmt.Errorf("failed to get total energy: %s", nvml.ErrorString(ret))
	}

	// Convert millijoules to device.Energy (which is in microjoules)
	return device.Energy(energyMJ) * device.MilliJoule, nil
}

// GetComputeRunningProcesses returns processes currently using the GPU for compute
func (d *nvmlDevice) GetComputeRunningProcesses() ([]gpu.ProcessGPUInfo, error) {
	procs, ret := d.handle.GetComputeRunningProcesses()
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("failed to get running processes: %s", nvml.ErrorString(ret))
	}

	now := time.Now()
	result := make([]gpu.ProcessGPUInfo, len(procs))
	for i, p := range procs {
		result[i] = gpu.ProcessGPUInfo{
			PID:         p.Pid,
			DeviceIndex: d.index,
			DeviceUUID:  d.uuid,
			MemoryUsed:  p.UsedGpuMemory,
			Timestamp:   now,
		}
	}

	return result, nil
}

// GetProcessUtilization returns per-process SM and memory utilization.
// This is the key API for time-slicing power attribution.
//
// Why NVML over DCGM for time-slicing:
// DCGM profiling (Field 1001) measures hardware counter residency during a process's
// time slice, often reporting 100% for each process. nvmlDeviceGetProcessUtilization
// correctly accounts for time-shared context switching at the driver level.
//
// Parameters:
//   - lastSeen: timestamp (microseconds) from previous call, or 0 for first call
//
// Returns slice of ProcessUtilization with SmUtil normalized across all processes.
func (d *nvmlDevice) GetProcessUtilization(lastSeen uint64) ([]gpu.ProcessUtilization, error) {
	// GetProcessUtilization requires either:
	// 1. Accounting mode enabled, OR
	// 2. Recent NVIDIA driver (450+) which supports it without accounting mode

	samples, ret := d.handle.GetProcessUtilization(lastSeen)
	if ret == nvml.SUCCESS {
		result := make([]gpu.ProcessUtilization, len(samples))
		for i, s := range samples {
			result[i] = gpu.ProcessUtilization{
				PID:       s.Pid,
				SmUtil:    s.SmUtil,
				MemUtil:   s.MemUtil,
				EncUtil:   s.EncUtil,
				DecUtil:   s.DecUtil,
				Timestamp: s.TimeStamp,
			}
		}
		return result, nil
	}

	// Check if accounting mode is the issue
	mode, accRet := d.handle.GetAccountingMode()
	if accRet == nvml.SUCCESS && mode == nvml.FEATURE_DISABLED {
		return nil, gpu.ErrProcessUtilizationUnavailable{
			Reason: "process utilization requires accounting mode or driver 450+; accounting mode is disabled",
		}
	}

	return nil, gpu.ErrProcessUtilizationUnavailable{
		Reason: fmt.Sprintf("GetProcessUtilization failed: %s", nvml.ErrorString(ret)),
	}
}

// GetComputeMode returns the GPU's compute mode configuration.
// This determines whether the GPU is configured for exclusive or shared access:
//   - ComputeModeDefault: Multiple processes can share the GPU (time-slicing)
//   - ComputeModeExclusiveProcess: Only one process can use the GPU
//   - ComputeModeExclusiveThread: Only one thread can use the GPU (legacy)
//   - ComputeModeProhibited: No compute processes allowed
func (d *nvmlDevice) GetComputeMode() (gpu.ComputeMode, error) {
	mode, ret := d.handle.GetComputeMode()
	if ret != nvml.SUCCESS {
		return gpu.ComputeModeDefault, fmt.Errorf("failed to get compute mode: %s", nvml.ErrorString(ret))
	}

	// Map NVML compute mode to our ComputeMode type
	switch mode {
	case nvml.COMPUTEMODE_DEFAULT:
		return gpu.ComputeModeDefault, nil
	case nvml.COMPUTEMODE_EXCLUSIVE_THREAD:
		return gpu.ComputeModeExclusiveThread, nil
	case nvml.COMPUTEMODE_EXCLUSIVE_PROCESS:
		return gpu.ComputeModeExclusiveProcess, nil
	case nvml.COMPUTEMODE_PROHIBITED:
		return gpu.ComputeModeProhibited, nil
	default:
		return gpu.ComputeModeDefault, nil
	}
}

// IsMIGEnabled checks if Multi-Instance GPU mode is enabled on this device
func (d *nvmlDevice) IsMIGEnabled() (bool, error) {
	currentMode, _, ret := d.handle.GetMigMode()
	if ret == nvml.ERROR_NOT_SUPPORTED {
		return false, nil
	}
	if ret != nvml.SUCCESS {
		return false, fmt.Errorf("failed to get MIG mode: %s", nvml.ErrorString(ret))
	}

	return currentMode == nvml.DEVICE_MIG_ENABLE, nil
}

// GetMIGInstances returns all MIG GPU instances on this device
func (d *nvmlDevice) GetMIGInstances() ([]MIGInstance, error) {
	migEnabled, err := d.IsMIGEnabled()
	if err != nil {
		return nil, err
	}
	if !migEnabled {
		return nil, gpu.ErrMIGNotSupported{DeviceIndex: d.index}
	}

	// Get GPU instances (max 7 for A100)
	gpuInstances, ret := d.handle.GetMigDeviceHandleByIndex(0)
	if ret != nvml.SUCCESS {
		// Try alternative approach - enumerate all possible GPU instances
		return d.enumerateMIGInstances()
	}

	// Get info for this GPU instance
	info, ret := gpuInstances.GetGpuInstanceId()
	if ret != nvml.SUCCESS {
		return nil, fmt.Errorf("failed to get GPU instance info: %s", nvml.ErrorString(ret))
	}

	return []MIGInstance{
		{
			GPUInstanceID: uint(info),
		},
	}, nil
}

// enumerateMIGInstances discovers MIG instances by iterating through possible indices
func (d *nvmlDevice) enumerateMIGInstances() ([]MIGInstance, error) {
	var instances []MIGInstance

	// Try to get MIG devices by index (up to 7 for A100)
	for i := 0; i < 7; i++ {
		migDevice, ret := d.handle.GetMigDeviceHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}

		giID, ret := migDevice.GetGpuInstanceId()
		if ret != nvml.SUCCESS {
			continue
		}

		instances = append(instances, MIGInstance{
			GPUInstanceID: uint(giID),
			EntityID:      uint(i),
		})
	}

	if len(instances) == 0 {
		return nil, fmt.Errorf("no MIG instances found")
	}

	return instances, nil
}

// GetMIGDeviceByInstanceID returns a MIG device by its GPU Instance ID.
// The returned NVMLDevice can be used to call GetProcessUtilization() for
// processes running within this specific MIG instance.
func (d *nvmlDevice) GetMIGDeviceByInstanceID(gpuInstanceID uint) (NVMLDevice, error) {
	migEnabled, err := d.IsMIGEnabled()
	if err != nil {
		return nil, err
	}
	if !migEnabled {
		return nil, gpu.ErrMIGNotSupported{DeviceIndex: d.index}
	}

	// Iterate through MIG devices to find the one with matching GPU Instance ID
	for i := 0; i < 7; i++ {
		migHandle, ret := d.handle.GetMigDeviceHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}

		giID, ret := migHandle.GetGpuInstanceId()
		if ret != nvml.SUCCESS {
			continue
		}

		if uint(giID) == gpuInstanceID {
			uuid, _ := migHandle.GetUUID()
			name, _ := migHandle.GetName()
			if name == "" {
				name = fmt.Sprintf("MIG-%d-%d", d.index, gpuInstanceID)
			}

			return &nvmlDevice{
				index:  d.index, // Parent GPU index
				handle: migHandle,
				uuid:   uuid,
				name:   name,
			}, nil
		}
	}

	return nil, fmt.Errorf("MIG instance with GPU Instance ID %d not found", gpuInstanceID)
}

// GetMaxMigDeviceCount returns the maximum number of MIG devices (slices) for this GPU.
// Returns 0 if MIG is not supported.
func (d *nvmlDevice) GetMaxMigDeviceCount() (int, error) {
	count, ret := d.handle.GetMaxMigDeviceCount()
	if ret == nvml.ERROR_NOT_SUPPORTED {
		return 0, nil
	}
	if ret != nvml.SUCCESS {
		return 0, fmt.Errorf("failed to get max MIG device count: %s", nvml.ErrorString(ret))
	}
	return count, nil
}
