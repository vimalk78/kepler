// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package gpu

import "fmt"

// Vendor represents the GPU manufacturer
type Vendor string

const (
	VendorNVIDIA  Vendor = "nvidia"
	VendorAMD     Vendor = "amd"
	VendorIntel   Vendor = "intel"
	VendorUnknown Vendor = "unknown"
)

// SharingMode represents how a GPU is shared among processes
type SharingMode int

const (
	// SharingModeUnknown indicates the sharing mode could not be determined
	SharingModeUnknown SharingMode = iota

	// SharingModeExclusive indicates a single process has exclusive access to the GPU.
	// Enabled via: nvidia-smi -c EXCLUSIVE_PROCESS
	// Power attribution: 100% to the single process.
	// TODO: For power attribution, this is identical to time-slicing with one process.
	SharingModeExclusive

	// SharingModeTimeSlicing indicates multiple processes share the GPU via time-slicing.
	// Power attribution: Proportional to SM utilization via NVML GetProcessUtilization().
	// Formula: P_process = P_board * (SmUtil_process / Sum(SmUtil_all))
	SharingModeTimeSlicing

	// SharingModeMIG indicates the GPU is partitioned using Multi-Instance GPU.
	// Power attribution: Uses DCGM profiling (Field 1001 GR_ENGINE_ACTIVE) with
	// activity-based split among processes within each MIG instance.
	// Formula: P_process = (P_board * MIG_slices/Total_slices) * (Activity_proc / Sum(Activity_in_instance))
	SharingModeMIG
)

// String returns a human-readable name for the sharing mode
func (m SharingMode) String() string {
	switch m {
	case SharingModeExclusive:
		return "exclusive"
	case SharingModeTimeSlicing:
		return "time-slicing"
	case SharingModeMIG:
		return "mig"
	default:
		return "unknown"
	}
}

// ProcessUtilization holds per-process GPU utilization metrics
type ProcessUtilization struct {
	PID       uint32
	SmUtil    uint32 // SM (Streaming Multiprocessor) utilization percentage (0-100)
	MemUtil   uint32 // Memory utilization percentage (0-100)
	EncUtil   uint32 // Encoder utilization percentage (0-100)
	DecUtil   uint32 // Decoder utilization percentage (0-100)
	Timestamp uint64 // Timestamp in microseconds
}

// ErrGPUNotFound is returned when a GPU device is not found
type ErrGPUNotFound struct {
	DeviceIndex int
}

func (e ErrGPUNotFound) Error() string {
	return fmt.Sprintf("GPU device not found: index %d", e.DeviceIndex)
}

// ErrGPUNotInitialized is returned when GPU operations are attempted before initialization
type ErrGPUNotInitialized struct{}

func (e ErrGPUNotInitialized) Error() string {
	return "GPU power meter not initialized"
}

// ErrMIGNotSupported is returned when MIG operations are attempted on non-MIG hardware
type ErrMIGNotSupported struct {
	DeviceIndex int
}

func (e ErrMIGNotSupported) Error() string {
	return fmt.Sprintf("MIG not supported on GPU device: index %d", e.DeviceIndex)
}

// ErrProcessUtilizationUnavailable is returned when per-process utilization cannot be obtained
type ErrProcessUtilizationUnavailable struct {
	Reason string
}

func (e ErrProcessUtilizationUnavailable) Error() string {
	return fmt.Sprintf("process utilization unavailable: %s", e.Reason)
}

// ComputeMode represents the GPU's compute mode configuration
// This determines whether the GPU allows multiple processes to share it
type ComputeMode int

const (
	// ComputeModeDefault allows multiple processes to share the GPU (time-slicing)
	ComputeModeDefault ComputeMode = 0

	// ComputeModeExclusiveThread allows only one compute thread (legacy mode)
	ComputeModeExclusiveThread ComputeMode = 1

	// ComputeModeExclusiveProcess allows only one compute process
	ComputeModeExclusiveProcess ComputeMode = 2

	// ComputeModeProhibited disallows compute processes
	ComputeModeProhibited ComputeMode = 3
)

// String returns a human-readable name for the compute mode
func (m ComputeMode) String() string {
	switch m {
	case ComputeModeDefault:
		return "default"
	case ComputeModeExclusiveThread:
		return "exclusive-thread"
	case ComputeModeExclusiveProcess:
		return "exclusive-process"
	case ComputeModeProhibited:
		return "prohibited"
	default:
		return "unknown"
	}
}
