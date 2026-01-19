// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package nvidia

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/sustainable-computing-io/kepler/internal/device"
	"github.com/sustainable-computing-io/kepler/internal/device/gpu"
)

// MockNVMLBackend is a mock implementation of NVMLBackend for testing
type MockNVMLBackend struct {
	mock.Mock
}

func (m *MockNVMLBackend) Init() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockNVMLBackend) Shutdown() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockNVMLBackend) DeviceCount() int {
	args := m.Called()
	return args.Int(0)
}

func (m *MockNVMLBackend) GetDevice(index int) (NVMLDevice, error) {
	args := m.Called(index)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(NVMLDevice), args.Error(1)
}

func (m *MockNVMLBackend) DiscoverDevices() ([]gpu.GPUDevice, error) {
	args := m.Called()
	return args.Get(0).([]gpu.GPUDevice), args.Error(1)
}

// MockNVMLDevice is a mock implementation of NVMLDevice for testing
type MockNVMLDevice struct {
	mock.Mock
}

func (m *MockNVMLDevice) Index() int {
	args := m.Called()
	return args.Int(0)
}

func (m *MockNVMLDevice) UUID() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockNVMLDevice) Name() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockNVMLDevice) GetPowerUsage() (device.Power, error) {
	args := m.Called()
	return args.Get(0).(device.Power), args.Error(1)
}

func (m *MockNVMLDevice) GetTotalEnergy() (device.Energy, error) {
	args := m.Called()
	return args.Get(0).(device.Energy), args.Error(1)
}

func (m *MockNVMLDevice) GetComputeRunningProcesses() ([]gpu.ProcessGPUInfo, error) {
	args := m.Called()
	return args.Get(0).([]gpu.ProcessGPUInfo), args.Error(1)
}

func (m *MockNVMLDevice) GetProcessUtilization(lastSeen uint64) ([]gpu.ProcessUtilization, error) {
	args := m.Called(lastSeen)
	return args.Get(0).([]gpu.ProcessUtilization), args.Error(1)
}

func (m *MockNVMLDevice) GetComputeMode() (gpu.ComputeMode, error) {
	args := m.Called()
	return args.Get(0).(gpu.ComputeMode), args.Error(1)
}

func (m *MockNVMLDevice) IsMIGEnabled() (bool, error) {
	args := m.Called()
	return args.Bool(0), args.Error(1)
}

func (m *MockNVMLDevice) GetMIGInstances() ([]MIGInstance, error) {
	args := m.Called()
	return args.Get(0).([]MIGInstance), args.Error(1)
}

func (m *MockNVMLDevice) GetMIGDeviceByInstanceID(gpuInstanceID uint) (NVMLDevice, error) {
	args := m.Called(gpuInstanceID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(NVMLDevice), args.Error(1)
}

func (m *MockNVMLDevice) GetMaxMigDeviceCount() (int, error) {
	args := m.Called()
	return args.Int(0), args.Error(1)
}

// MockDCGMBackend is a mock implementation of DCGMBackend for testing
type MockDCGMBackend struct {
	mock.Mock
}

func (m *MockDCGMBackend) Init() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockDCGMBackend) Shutdown() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockDCGMBackend) IsInitialized() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *MockDCGMBackend) GetMIGHierarchy(totalGPUSlices uint) (*MIGHierarchy, error) {
	args := m.Called(totalGPUSlices)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*MIGHierarchy), args.Error(1)
}

func (m *MockDCGMBackend) GetMIGInstanceActivity(gpuIndex int, gpuInstanceID uint) (float64, error) {
	args := m.Called(gpuIndex, gpuInstanceID)
	return args.Get(0).(float64), args.Error(1)
}

func (m *MockDCGMBackend) GetMIGInstancesForGPU(gpuIndex int, totalGPUSlices uint) ([]MIGGPUInstance, error) {
	args := m.Called(gpuIndex, totalGPUSlices)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]MIGGPUInstance), args.Error(1)
}

// mockSharingModeDetector is a simple mock for the SharingModeDetector interface
type mockSharingModeDetector struct {
	modes map[int]gpu.SharingMode
}

func newMockSharingModeDetector(modes map[int]gpu.SharingMode) *mockSharingModeDetector {
	return &mockSharingModeDetector{modes: modes}
}

func (d *mockSharingModeDetector) DetectMode(deviceIndex int) (gpu.SharingMode, error) {
	if mode, ok := d.modes[deviceIndex]; ok {
		return mode, nil
	}
	return gpu.SharingModeUnknown, nil
}

func (d *mockSharingModeDetector) DetectAllModes() (map[int]gpu.SharingMode, error) {
	return d.modes, nil
}

func (d *mockSharingModeDetector) Refresh() error {
	return nil
}

func TestGPUPowerCollector_ExclusiveMode(t *testing.T) {
	// Setup mocks
	mockNVML := new(MockNVMLBackend)
	mockDevice := new(MockNVMLDevice)
	mockDCGM := new(MockDCGMBackend)

	// Configure NVML mock - only set up what's actually called
	mockNVML.On("GetDevice", 0).Return(mockDevice, nil)

	// Configure device mock for exclusive mode
	mockDevice.On("GetComputeRunningProcesses").Return([]gpu.ProcessGPUInfo{
		{PID: 1234, DeviceIndex: 0, Timestamp: time.Now()},
	}, nil)
	mockDevice.On("GetPowerUsage").Return(device.Power(250*device.Watt), nil)

	// DCGM not needed for exclusive mode
	mockDCGM.On("IsInitialized").Return(false)

	// Create collector
	collector := NewGPUPowerCollector(
		WithLogger(slog.Default()),
		WithNVMLBackend(mockNVML),
		WithDCGMBackend(mockDCGM),
	)

	// Manually set up the collector state (bypass full Init)
	collector.initialized = true
	collector.devices = []gpu.GPUDevice{{Index: 0, UUID: "GPU-0", Name: "Tesla V100"}}
	collector.sharingModes = map[int]gpu.SharingMode{0: gpu.SharingModeExclusive}

	// Test GetProcessPower
	result, err := collector.GetProcessPower()
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.InDelta(t, 250.0, result[1234], 0.01, "Single process should get 100% of power")

	mockNVML.AssertExpectations(t)
	mockDevice.AssertExpectations(t)
}

func TestGPUPowerCollector_TimeSlicingMode(t *testing.T) {
	// Setup mocks
	mockNVML := new(MockNVMLBackend)
	mockDevice := new(MockNVMLDevice)
	mockDCGM := new(MockDCGMBackend)

	// Configure NVML mock - only set up what's actually called
	mockNVML.On("GetDevice", 0).Return(mockDevice, nil)

	// Configure device mock for time-slicing mode
	mockDevice.On("GetPowerUsage").Return(device.Power(300*device.Watt), nil)

	// Running processes (authoritative list)
	mockDevice.On("GetComputeRunningProcesses").Return([]gpu.ProcessGPUInfo{
		{PID: 1001, DeviceIndex: 0},
		{PID: 1002, DeviceIndex: 0},
	}, nil)

	// Two processes sharing the GPU via time-slicing
	// Process 1: 60% SM utilization, Process 2: 40% SM utilization
	mockDevice.On("GetProcessUtilization", mock.Anything).Return([]gpu.ProcessUtilization{
		{PID: 1001, SmUtil: 60, Timestamp: 1000},
		{PID: 1002, SmUtil: 40, Timestamp: 1000},
	}, nil)

	// DCGM not needed for time-slicing
	mockDCGM.On("IsInitialized").Return(false)

	// Create collector
	collector := NewGPUPowerCollector(
		WithLogger(slog.Default()),
		WithNVMLBackend(mockNVML),
		WithDCGMBackend(mockDCGM),
	)

	// Manually set up the collector state
	collector.initialized = true
	collector.devices = []gpu.GPUDevice{{Index: 0, UUID: "GPU-0", Name: "Tesla V100"}}
	collector.sharingModes = map[int]gpu.SharingMode{0: gpu.SharingModeTimeSlicing}
	collector.minObservedPower = map[int]float64{0: 50.0} // Set idle power to 50W (simulating prior observation)
	collector.idlePower = make(map[int]float64)

	// Test GetProcessPower
	// Active power = 300W - 50W idle = 250W
	result, err := collector.GetProcessPower()
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	// Process 1001: 60% of 250W active = 150W
	assert.InDelta(t, 150.0, result[1001], 0.01, "Process 1001 should get 60% of active power")
	// Process 1002: 40% of 250W active = 100W
	assert.InDelta(t, 100.0, result[1002], 0.01, "Process 1002 should get 40% of active power")

	mockNVML.AssertExpectations(t)
	mockDevice.AssertExpectations(t)
}

func TestGPUPowerCollector_FallbackAttribution(t *testing.T) {
	// Test fallback when process utilization is unavailable
	mockNVML := new(MockNVMLBackend)
	mockDevice := new(MockNVMLDevice)
	mockDCGM := new(MockDCGMBackend)

	// Configure only what's actually called
	mockNVML.On("GetDevice", 0).Return(mockDevice, nil)

	mockDevice.On("GetPowerUsage").Return(device.Power(200*device.Watt), nil)

	// Process utilization unavailable - triggers fallback
	mockDevice.On("GetProcessUtilization", mock.Anything).Return(
		[]gpu.ProcessUtilization{},
		gpu.ErrProcessUtilizationUnavailable{Reason: "test"},
	)

	// Fallback uses GetComputeRunningProcesses for equal distribution
	mockDevice.On("GetComputeRunningProcesses").Return([]gpu.ProcessGPUInfo{
		{PID: 2001, DeviceIndex: 0},
		{PID: 2002, DeviceIndex: 0},
	}, nil)

	mockDCGM.On("IsInitialized").Return(false)

	collector := NewGPUPowerCollector(
		WithLogger(slog.Default()),
		WithNVMLBackend(mockNVML),
		WithDCGMBackend(mockDCGM),
	)

	collector.initialized = true
	collector.devices = []gpu.GPUDevice{{Index: 0, UUID: "GPU-0", Name: "Tesla V100"}}
	collector.sharingModes = map[int]gpu.SharingMode{0: gpu.SharingModeTimeSlicing}
	collector.minObservedPower = map[int]float64{0: 20.0} // Set idle power to 20W
	collector.idlePower = make(map[int]float64)

	result, err := collector.GetProcessPower()
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	// Equal distribution of active power: (200W - 20W idle) / 2 = 90W each
	assert.InDelta(t, 90.0, result[2001], 0.01)
	assert.InDelta(t, 90.0, result[2002], 0.01)
}

func TestGPUPowerCollector_ConcurrentAccess(t *testing.T) {
	// Test thread safety of GetProcessPower
	mockNVML := new(MockNVMLBackend)
	mockDevice := new(MockNVMLDevice)
	mockDCGM := new(MockDCGMBackend)

	// Configure only what's actually called
	mockNVML.On("GetDevice", 0).Return(mockDevice, nil)

	mockDevice.On("GetPowerUsage").Return(device.Power(300*device.Watt), nil)
	mockDevice.On("GetComputeRunningProcesses").Return([]gpu.ProcessGPUInfo{
		{PID: 3001, DeviceIndex: 0},
	}, nil)

	mockDCGM.On("IsInitialized").Return(false)

	collector := NewGPUPowerCollector(
		WithLogger(slog.Default()),
		WithNVMLBackend(mockNVML),
		WithDCGMBackend(mockDCGM),
	)

	collector.initialized = true
	collector.devices = []gpu.GPUDevice{{Index: 0, UUID: "GPU-0", Name: "Tesla V100"}}
	collector.sharingModes = map[int]gpu.SharingMode{0: gpu.SharingModeExclusive}

	// Run concurrent calls
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := collector.GetProcessPower()
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent access error: %v", err)
	}
}

func TestGPUPowerCollector_NotInitialized(t *testing.T) {
	collector := NewGPUPowerCollector()

	_, err := collector.GetProcessPower()
	assert.Error(t, err)
	assert.IsType(t, gpu.ErrGPUNotInitialized{}, err)
}

func TestGPUPowerCollector_NoProcesses(t *testing.T) {
	mockNVML := new(MockNVMLBackend)
	mockDevice := new(MockNVMLDevice)
	mockDCGM := new(MockDCGMBackend)

	mockNVML.On("GetDevice", 0).Return(mockDevice, nil)
	mockDevice.On("GetPowerUsage").Return(device.Power(100*device.Watt), nil)
	mockDevice.On("GetComputeRunningProcesses").Return([]gpu.ProcessGPUInfo{}, nil)
	mockDCGM.On("IsInitialized").Return(false)

	collector := NewGPUPowerCollector(
		WithNVMLBackend(mockNVML),
		WithDCGMBackend(mockDCGM),
	)

	collector.initialized = true
	collector.devices = []gpu.GPUDevice{{Index: 0}}
	collector.sharingModes = map[int]gpu.SharingMode{0: gpu.SharingModeExclusive}

	result, err := collector.GetProcessPower()
	assert.NoError(t, err)
	assert.Empty(t, result, "No processes should mean empty result")
}

func TestSharingMode_String(t *testing.T) {
	tests := []struct {
		mode     gpu.SharingMode
		expected string
	}{
		{gpu.SharingModeExclusive, "exclusive"},
		{gpu.SharingModeTimeSlicing, "time-slicing"},
		{gpu.SharingModeMIG, "mig"},
		{gpu.SharingModeUnknown, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.mode.String())
		})
	}
}

func TestVendor_Constants(t *testing.T) {
	assert.Equal(t, gpu.Vendor("nvidia"), gpu.VendorNVIDIA)
	assert.Equal(t, gpu.Vendor("amd"), gpu.VendorAMD)
	assert.Equal(t, gpu.Vendor("intel"), gpu.VendorIntel)
}
