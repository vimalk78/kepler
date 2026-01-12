// SPDX-FileCopyrightText: 2025 The Kepler Authors
// SPDX-License-Identifier: Apache-2.0

package nvidia

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/sustainable-computing-io/kepler/internal/device/gpu"
)

// DCGMExporterBackend provides MIG metrics by querying dcgm-exporter's Prometheus endpoint.
// This is an alternative to the go-dcgm library that doesn't require libdcgm.so.
//
// Why dcgm-exporter:
// - dcgm-exporter is already deployed by NVIDIA GPU Operator as a DaemonSet
// - Exposes per-MIG-instance metrics with GPU_I_ID and GPU_I_PROFILE labels
// - No native library dependencies required
// - Uses standard Prometheus text format
// metricsCacheTTL is how long cached metrics are valid before refetching.
// This prevents HTTP request storms when querying multiple MIG instances.
const metricsCacheTTL = 2 * time.Second

type DCGMExporterBackend struct {
	logger      *slog.Logger
	endpoint    string // e.g., "http://10.131.2.22:9400/metrics"
	client      *http.Client
	initialized bool
	mu          sync.RWMutex

	// Cached metrics with TTL to avoid HTTP request storms
	cachedMetrics *dcgmMetrics
}

// dcgmMetrics holds parsed metrics from dcgm-exporter
type dcgmMetrics struct {
	// MIG instances indexed by (gpuIndex, gpuInstanceID)
	instances map[migKey]*migInstanceMetrics
	timestamp time.Time
}

type migKey struct {
	gpuIndex      int
	gpuInstanceID uint
}

type migInstanceMetrics struct {
	gpuIndex      int
	gpuInstanceID uint
	profile       string  // e.g., "1g.5gb"
	activity      float64 // DCGM_FI_PROF_GR_ENGINE_ACTIVE
	powerUsage    float64 // DCGM_FI_DEV_POWER_USAGE
}

// NewDCGMExporterBackend creates a new dcgm-exporter HTTP backend
func NewDCGMExporterBackend(logger *slog.Logger) *DCGMExporterBackend {
	if logger == nil {
		logger = slog.Default()
	}
	return &DCGMExporterBackend{
		logger: logger.With("component", "dcgm-exporter"),
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// WithEndpoint sets the dcgm-exporter endpoint URL
func (d *DCGMExporterBackend) WithEndpoint(endpoint string) *DCGMExporterBackend {
	d.endpoint = endpoint
	return d
}

// Init initializes the backend by discovering the dcgm-exporter endpoint.
// If endpoint is not set, it discovers the local dcgm-exporter pod on the same node.
func (d *DCGMExporterBackend) Init() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.initialized {
		return nil
	}

	// If endpoint is already set, use it
	if d.endpoint != "" {
		if err := d.testEndpoint(d.endpoint); err != nil {
			return fmt.Errorf("configured endpoint %s not reachable: %w", d.endpoint, err)
		}
		d.initialized = true
		d.logger.Info("DCGM exporter backend initialized", "endpoint", d.endpoint)
		return nil
	}

	// Try to discover dcgm-exporter endpoint
	// Priority:
	// 1. Local dcgm-exporter pod on same node (discovered via K8s API)
	// 2. localhost:9400 (if dcgm-exporter uses hostNetwork)
	// 3. ClusterIP service (may route to wrong node - last resort)

	// Try to discover local pod first
	if localEndpoint := d.discoverLocalDCGMExporter(); localEndpoint != "" {
		if err := d.testEndpoint(localEndpoint); err == nil {
			d.endpoint = localEndpoint
			d.initialized = true
			d.logger.Info("DCGM exporter backend initialized", "endpoint", localEndpoint, "discovery", "local-pod")
			return nil
		}
		d.logger.Debug("local dcgm-exporter not reachable", "endpoint", localEndpoint)
	}

	// Fallback to static endpoints
	endpoints := []string{
		"http://localhost:9400/metrics",
		"http://nvidia-dcgm-exporter.nvidia-gpu-operator.svc:9400/metrics",
	}

	for _, ep := range endpoints {
		if err := d.testEndpoint(ep); err == nil {
			d.endpoint = ep
			d.initialized = true
			d.logger.Info("DCGM exporter backend initialized", "endpoint", ep, "discovery", "fallback")
			return nil
		}
		d.logger.Debug("dcgm-exporter endpoint not reachable", "endpoint", ep)
	}

	return fmt.Errorf("no reachable dcgm-exporter endpoint found")
}

// discoverLocalDCGMExporter finds the dcgm-exporter pod IP on the same node
func (d *DCGMExporterBackend) discoverLocalDCGMExporter() string {
	// Get current node name from environment (set by downward API)
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		d.logger.Debug("NODE_NAME not set, cannot discover local dcgm-exporter")
		return ""
	}

	// Create in-cluster Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		d.logger.Debug("failed to get in-cluster config", "error", err)
		return ""
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		d.logger.Debug("failed to create kubernetes client", "error", err)
		return ""
	}

	// Find dcgm-exporter pod on the same node
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pods, err := clientset.CoreV1().Pods("nvidia-gpu-operator").List(ctx, metav1.ListOptions{
		LabelSelector: "app=nvidia-dcgm-exporter",
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeName),
	})
	if err != nil {
		d.logger.Debug("failed to list dcgm-exporter pods", "error", err)
		return ""
	}

	if len(pods.Items) == 0 {
		d.logger.Debug("no dcgm-exporter pod found on node", "node", nodeName)
		return ""
	}

	pod := pods.Items[0]
	if pod.Status.PodIP == "" {
		d.logger.Debug("dcgm-exporter pod has no IP", "pod", pod.Name)
		return ""
	}

	endpoint := fmt.Sprintf("http://%s:9400/metrics", pod.Status.PodIP)
	d.logger.Debug("discovered local dcgm-exporter", "pod", pod.Name, "ip", pod.Status.PodIP, "node", nodeName)
	return endpoint
}

// testEndpoint checks if an endpoint is reachable and returns valid metrics
func (d *DCGMExporterBackend) testEndpoint(endpoint string) error {
	resp, err := d.client.Get(endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Read a bit to verify it's valid
	buf := make([]byte, 1024)
	_, err = resp.Body.Read(buf)
	if err != nil && err != io.EOF {
		return err
	}

	return nil
}

// Shutdown cleans up resources
func (d *DCGMExporterBackend) Shutdown() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.initialized = false
	d.cachedMetrics = nil
	d.logger.Info("DCGM exporter backend shutdown")
	return nil
}

// IsInitialized returns whether the backend is ready
func (d *DCGMExporterBackend) IsInitialized() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.initialized
}

// GetMIGHierarchy returns the MIG topology parsed from dcgm-exporter metrics.
// The hierarchy is built from DCGM_FI_DEV_POWER_USAGE metrics with GPU_I_ID labels.
func (d *DCGMExporterBackend) GetMIGHierarchy(totalGPUSlices uint) (*MIGHierarchy, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if !d.initialized {
		return nil, gpu.ErrGPUNotInitialized{}
	}

	metrics, err := d.fetchMetrics()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics: %w", err)
	}

	hierarchy := &MIGHierarchy{
		GPUInstances: make([]MIGGPUInstance, 0, len(metrics.instances)),
	}

	for _, m := range metrics.instances {
		slices := parseProfileSlices(m.profile)
		hierarchy.GPUInstances = append(hierarchy.GPUInstances, MIGGPUInstance{
			ParentGPUIndex:     m.gpuIndex,
			GPUInstanceID:      m.gpuInstanceID,
			EntityID:           m.gpuInstanceID,
			ProfileSlices:      slices,
			TotalGPUSlices:     totalGPUSlices,
			ComputeInstanceIDs: []uint{},
		})
	}

	return hierarchy, nil
}

// parseProfileSlices extracts the slice count from a profile name like "1g.5gb", "3g.20gb"
func parseProfileSlices(profile string) uint {
	// Profile format: "<slices>g.<memory>gb" e.g., "1g.5gb", "2g.10gb", "3g.20gb"
	if len(profile) == 0 {
		return 1
	}
	if profile[0] >= '1' && profile[0] <= '9' {
		return uint(profile[0] - '0')
	}
	return 1
}

// GetMIGInstancesForGPU returns the MIG instances for a specific GPU from cached metrics.
// This is called per-collection instead of caching at startup, since dcgm-exporter only
// reports metrics for MIG instances with active workloads.
func (d *DCGMExporterBackend) GetMIGInstancesForGPU(gpuIndex int, totalGPUSlices uint) ([]MIGGPUInstance, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if !d.initialized {
		return nil, gpu.ErrGPUNotInitialized{}
	}

	metrics, err := d.fetchMetrics()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metrics: %w", err)
	}

	var instances []MIGGPUInstance
	for key, m := range metrics.instances {
		if key.gpuIndex == gpuIndex {
			slices := parseProfileSlices(m.profile)
			instances = append(instances, MIGGPUInstance{
				ParentGPUIndex:     gpuIndex,
				GPUInstanceID:      m.gpuInstanceID,
				EntityID:           m.gpuInstanceID,
				ProfileSlices:      slices,
				TotalGPUSlices:     totalGPUSlices,
				ComputeInstanceIDs: []uint{},
			})
		}
	}

	return instances, nil
}

// GetMIGInstanceActivity returns the GR_ENGINE_ACTIVE metric for a MIG instance.
func (d *DCGMExporterBackend) GetMIGInstanceActivity(gpuIndex int, gpuInstanceID uint) (float64, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if !d.initialized {
		return 0, gpu.ErrGPUNotInitialized{}
	}

	metrics, err := d.fetchMetrics()
	if err != nil {
		return 0, fmt.Errorf("failed to fetch metrics: %w", err)
	}

	key := migKey{gpuIndex: gpuIndex, gpuInstanceID: gpuInstanceID}
	if m, ok := metrics.instances[key]; ok {
		return m.activity, nil
	}

	return 0, fmt.Errorf("no metrics found for GPU %d, MIG instance %d", gpuIndex, gpuInstanceID)
}

// fetchMetrics returns cached metrics if still valid, otherwise fetches fresh data.
// This prevents HTTP request storms when querying multiple MIG instances in a single collection cycle.
func (d *DCGMExporterBackend) fetchMetrics() (*dcgmMetrics, error) {
	// Return cached metrics if still valid
	if d.cachedMetrics != nil && time.Since(d.cachedMetrics.timestamp) < metricsCacheTTL {
		return d.cachedMetrics, nil
	}

	resp, err := d.client.Get(d.endpoint)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	metrics, err := d.parseMetrics(resp.Body)
	if err != nil {
		return nil, err
	}

	// Cache the metrics
	d.cachedMetrics = metrics
	return metrics, nil
}

// parseMetrics parses Prometheus text format metrics from dcgm-exporter
func (d *DCGMExporterBackend) parseMetrics(reader io.Reader) (*dcgmMetrics, error) {
	metrics := &dcgmMetrics{
		instances: make(map[migKey]*migInstanceMetrics),
		timestamp: time.Now(),
	}

	// Regex to parse Prometheus metric lines
	// Format: metric_name{label1="value1",label2="value2",...} value
	metricRegex := regexp.MustCompile(`^(DCGM_FI_\w+)\{([^}]+)\}\s+([0-9.eE+-]+)`)

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()

		// Skip comments and empty lines
		if len(line) == 0 || line[0] == '#' {
			continue
		}

		matches := metricRegex.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		metricName := matches[1]
		labelsStr := matches[2]
		valueStr := matches[3]

		// Parse labels
		labels := parseLabels(labelsStr)

		// Only process metrics with GPU_I_ID (MIG instance metrics)
		gpuIIDStr, hasMIG := labels["GPU_I_ID"]
		if !hasMIG {
			continue
		}

		gpuStr := labels["gpu"]
		gpuIndex, _ := strconv.Atoi(gpuStr)
		gpuInstanceID, _ := strconv.ParseUint(gpuIIDStr, 10, 32)

		key := migKey{gpuIndex: gpuIndex, gpuInstanceID: uint(gpuInstanceID)}

		// Get or create instance metrics
		instance, ok := metrics.instances[key]
		if !ok {
			instance = &migInstanceMetrics{
				gpuIndex:      gpuIndex,
				gpuInstanceID: uint(gpuInstanceID),
				profile:       labels["GPU_I_PROFILE"],
			}
			metrics.instances[key] = instance
		}

		// Parse value
		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue
		}

		// Store relevant metrics
		switch metricName {
		case "DCGM_FI_PROF_GR_ENGINE_ACTIVE":
			instance.activity = value
		case "DCGM_FI_DEV_POWER_USAGE":
			instance.powerUsage = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading metrics: %w", err)
	}

	return metrics, nil
}

// parseLabels parses a Prometheus label string like `key1="val1",key2="val2"`
func parseLabels(labelsStr string) map[string]string {
	labels := make(map[string]string)

	// Split by comma, but be careful with quoted values containing commas
	// Simple approach: regex for each label
	labelRegex := regexp.MustCompile(`(\w+)="([^"]*)"`)
	matches := labelRegex.FindAllStringSubmatch(labelsStr, -1)

	for _, match := range matches {
		if len(match) == 3 {
			labels[match[1]] = match[2]
		}
	}

	return labels
}

// SetEndpoint allows setting the endpoint after creation (for testing or configuration)
func (d *DCGMExporterBackend) SetEndpoint(endpoint string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Ensure endpoint has /metrics suffix
	if !strings.HasSuffix(endpoint, "/metrics") {
		endpoint = strings.TrimSuffix(endpoint, "/") + "/metrics"
	}
	d.endpoint = endpoint
}
