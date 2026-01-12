// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package nvidia

import (
	"log/slog"

	"github.com/sustainable-computing-io/kepler/internal/device/gpu"
)

// SharingModeDetector determines the GPU sharing mode at runtime.
// This is critical for selecting the correct power attribution strategy:
//
//   - MIG mode: Use DCGM profiling (Field 1001) because NVML returns N/A
//   - Time-slicing: Use NVML GetProcessUtilization() which correctly handles
//     context switching (unlike DCGM which reports 100% per process)
//   - Exclusive: Use NVML with 100% attribution to single process
type SharingModeDetector interface {
	// DetectMode determines the sharing mode for a specific GPU device
	DetectMode(deviceIndex int) (gpu.SharingMode, error)

	// DetectAllModes returns the sharing mode for all devices
	DetectAllModes() (map[int]gpu.SharingMode, error)

	// Refresh re-detects modes for all devices (call when processes change)
	Refresh() error
}

// sharingModeDetector is the concrete implementation of SharingModeDetector
type sharingModeDetector struct {
	logger      *slog.Logger
	nvml        NVMLBackend
	cachedModes map[int]gpu.SharingMode
}

// NewSharingModeDetector creates a new GPU sharing mode detector
func NewSharingModeDetector(logger *slog.Logger, nvml NVMLBackend) SharingModeDetector {
	if logger == nil {
		logger = slog.Default()
	}
	return &sharingModeDetector{
		logger:      logger.With("component", "gpu-sharing-mode-detector"),
		nvml:        nvml,
		cachedModes: make(map[int]gpu.SharingMode),
	}
}

// DetectMode determines the sharing mode for a specific GPU device.
//
// Detection logic (in order):
//  1. Check if MIG is enabled -> SharingModeMIG
//  2. Check GPU compute mode via NVML:
//     - EXCLUSIVE_PROCESS -> SharingModeExclusive
//     - DEFAULT (shared) -> SharingModeTimeSlicing
func (d *sharingModeDetector) DetectMode(deviceIndex int) (gpu.SharingMode, error) {
	device, err := d.nvml.GetDevice(deviceIndex)
	if err != nil {
		return gpu.SharingModeUnknown, err
	}

	// Step 1: Check for MIG mode first
	// MIG takes precedence because it fundamentally changes how the GPU operates
	migEnabled, err := device.IsMIGEnabled()
	if err != nil {
		d.logger.Warn("failed to check MIG mode, assuming disabled",
			"device", deviceIndex, "error", err)
		migEnabled = false
	}

	if migEnabled {
		d.logger.Debug("detected MIG mode", "device", deviceIndex)
		return gpu.SharingModeMIG, nil
	}

	// Step 2: Check compute mode via NVML
	// This is more reliable than counting processes because it reflects
	// the GPU's configured mode, not just current state
	computeMode, err := device.GetComputeMode()
	if err != nil {
		d.logger.Warn("failed to get compute mode, defaulting to time-slicing",
			"device", deviceIndex, "error", err)
		// Default to time-slicing - this is the GPU factory default (ComputeMode = DEFAULT)
		// Time-slicing attribution also handles single process correctly:
		// P = P_total * (util/total_util) = 100% when only one process
		return gpu.SharingModeTimeSlicing, nil
	}

	d.logger.Debug("detected compute mode", "device", deviceIndex, "mode", computeMode)

	// ComputeMode values:
	// - EXCLUSIVE_PROCESS (2): Only one process can use the GPU
	// - EXCLUSIVE_THREAD (1): Only one thread can use the GPU (legacy)
	// - DEFAULT (0): Multiple processes can share the GPU (time-slicing)
	// - PROHIBITED (3): No compute processes allowed
	switch computeMode {
	case gpu.ComputeModeExclusiveProcess, gpu.ComputeModeExclusiveThread:
		return gpu.SharingModeExclusive, nil
	default:
		// DEFAULT or PROHIBITED - treat as time-slicing capable
		return gpu.SharingModeTimeSlicing, nil
	}
}

// DetectAllModes returns the sharing mode for all discovered GPU devices
func (d *sharingModeDetector) DetectAllModes() (map[int]gpu.SharingMode, error) {
	modes := make(map[int]gpu.SharingMode)

	deviceCount := d.nvml.DeviceCount()
	for i := range deviceCount {
		mode, err := d.DetectMode(i)
		if err != nil {
			d.logger.Warn("failed to detect mode for device",
				"device", i, "error", err)
			mode = gpu.SharingModeUnknown
		}
		modes[i] = mode
	}

	d.cachedModes = modes
	return modes, nil
}

// Refresh re-detects the sharing mode for all devices.
// Call this periodically or when process changes are detected.
func (d *sharingModeDetector) Refresh() error {
	_, err := d.DetectAllModes()
	return err
}

// GetCachedMode returns the last detected mode for a device without re-checking.
// Returns SharingModeUnknown if the device hasn't been checked.
func (d *sharingModeDetector) GetCachedMode(deviceIndex int) gpu.SharingMode {
	if mode, ok := d.cachedModes[deviceIndex]; ok {
		return mode
	}
	return gpu.SharingModeUnknown
}
