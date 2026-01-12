// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package nvidia

// DCGMBackend provides access to MIG metrics for power attribution.
//
// Why we need MIG-specific metrics:
// When MIG is enabled, standard NVML utilization queries return "N/A" because
// the physical GPU is partitioned into isolated GPU Instances. We need to query
// metrics at the GPU Instance level to attribute power to processes.
//
// The key metric is DCGM_FI_PROF_GR_ENGINE_ACTIVE (Field 1001) which measures
// the graphics/compute engine activity ratio (0.0-1.0) for each MIG instance.
//
// Implementations:
//   - DCGMExporterBackend: Queries dcgm-exporter's Prometheus endpoint (no library dependency)
type DCGMBackend interface {
	Init() error
	Shutdown() error
	IsInitialized() bool

	// GetMIGHierarchy returns the MIG topology (GPU instances and compute instances).
	// totalGPUSlices should be obtained from NVML GetMaxMigDeviceCount().
	GetMIGHierarchy(totalGPUSlices uint) (*MIGHierarchy, error)

	// GetMIGInstanceActivity returns the GR_ENGINE_ACTIVE metric for a MIG instance.
	// Returns a value 0.0-1.0 representing the activity ratio.
	GetMIGInstanceActivity(gpuIndex int, gpuInstanceID uint) (float64, error)

	// GetMIGInstancesForGPU returns MIG instances for a specific GPU from cached metrics.
	// This is called per-collection since dcgm-exporter only reports metrics for active instances.
	GetMIGInstancesForGPU(gpuIndex int, totalGPUSlices uint) ([]MIGGPUInstance, error)
}

// MIGHierarchy represents the MIG device topology
type MIGHierarchy struct {
	GPUInstances []MIGGPUInstance
}

// MIGGPUInstance represents a GPU Instance in MIG mode
type MIGGPUInstance struct {
	// ParentGPUIndex is the index of the physical GPU that contains this MIG instance
	ParentGPUIndex int

	// GPUInstanceID is the unique identifier for this GPU Instance within the parent GPU
	GPUInstanceID uint

	// EntityID is the DCGM entity ID used for querying metrics (FE_GPU_I entity type)
	EntityID uint

	// ProfileSlices is the number of GPU slices allocated to this instance
	// (e.g., 3 for a 3g.20gb profile on A100)
	ProfileSlices uint

	// TotalGPUSlices is the total number of slices on the parent GPU
	// (e.g., 7 for A100, 8 for H100)
	TotalGPUSlices uint

	// ComputeInstanceIDs contains the IDs of compute instances within this GPU instance
	ComputeInstanceIDs []uint
}
