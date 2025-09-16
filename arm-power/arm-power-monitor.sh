#!/bin/bash
# ARM Power Monitor Script for Bare Metal ARM Servers
# Optimized for Ampere eMAG processors
# Usage: ./arm-power-monitor.sh [scan|monitor|json]

set -euo pipefail

VERSION="1.0"
SCRIPT_NAME="ARM Power Monitor"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
print_header() {
    echo -e "${BLUE}=== $1 ===${NC}"
}

print_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to check if running with sufficient privileges
check_privileges() {
    if [[ $EUID -eq 0 ]]; then
        print_info "Running as root - full hardware access available"
    else
        print_warning "Running as non-root user - some sensors may not be accessible"
        print_info "For full access, run: sudo $0 $*"
    fi
}

# Function to detect ARM processor type
detect_arm_processor() {
    print_header "ARM Processor Detection"

    if [[ -f /proc/cpuinfo ]]; then
        local vendor=$(grep -m1 "CPU implementer" /proc/cpuinfo | cut -d: -f2 | xargs)
        local part=$(grep -m1 "CPU part" /proc/cpuinfo | cut -d: -f2 | xargs)
        local model=$(grep -m1 "model name" /proc/cpuinfo | cut -d: -f2 | xargs)

        echo "Architecture: $(uname -m)"
        echo "CPU Count: $(nproc)"

        if [[ -n "$vendor" ]]; then
            case "$vendor" in
                "0x41") echo "Vendor: ARM Ltd" ;;
                "0x42") echo "Vendor: Broadcom" ;;
                "0x43") echo "Vendor: Cavium" ;;
                "0x44") echo "Vendor: Digital Equipment Corp" ;;
                "0x46") echo "Vendor: Fujitsu" ;;
                "0x48") echo "Vendor: HiSilicon" ;;
                "0x49") echo "Vendor: Infineon" ;;
                "0x4d") echo "Vendor: Motorola" ;;
                "0x4e") echo "Vendor: NVIDIA" ;;
                "0x50") echo "Vendor: Applied Micro (APM/Ampere)" ;;
                "0x51") echo "Vendor: Qualcomm" ;;
                "0x56") echo "Vendor: Marvell" ;;
                *) echo "Vendor: Unknown ($vendor)" ;;
            esac
        fi

        if [[ -n "$model" ]]; then
            echo "Model: $model"
        fi

        # Check for Ampere eMAG specific features
        if grep -q "eMAG" /proc/cpuinfo; then
            print_info "Detected Ampere eMAG processor - optimizing for server-grade ARM"
        fi

        # Display CPU features
        local features=$(grep -m1 "Features" /proc/cpuinfo | cut -d: -f2 | xargs)
        if [[ -n "$features" ]]; then
            echo "Features: $features"
        fi
    else
        print_error "Cannot read /proc/cpuinfo"
    fi
    echo
}

# Function to scan for hardware monitoring interfaces
scan_hwmon_sensors() {
    print_header "Hardware Monitoring Sensors (hwmon)"

    local found_sensors=0

    if [[ -d /sys/class/hwmon ]]; then
        for hwmon in /sys/class/hwmon/hwmon*; do
            if [[ -d "$hwmon" ]]; then
                found_sensors=1
                local name=""
                if [[ -f "$hwmon/name" ]]; then
                    name=$(cat "$hwmon/name" 2>/dev/null || echo "unknown")
                fi

                echo "Sensor: $(basename "$hwmon") ($name)"

                # Check for temperature sensors
                for temp in "$hwmon"/temp*_input; do
                    if [[ -f "$temp" ]]; then
                        local temp_val=$(cat "$temp" 2>/dev/null || echo "0")
                        local temp_c=$((temp_val / 1000))
                        local label_file="${temp%_input}_label"
                        local label="Temperature"
                        if [[ -f "$label_file" ]]; then
                            label=$(cat "$label_file" 2>/dev/null || echo "Temperature")
                        fi
                        echo "  $label: ${temp_c}°C"
                    fi
                done

                # Check for voltage sensors
                for voltage in "$hwmon"/in*_input; do
                    if [[ -f "$voltage" ]]; then
                        local volt_val=$(cat "$voltage" 2>/dev/null || echo "0")
                        local volt_v=$(echo "scale=3; $volt_val / 1000" | bc -l 2>/dev/null || echo "0")
                        local label_file="${voltage%_input}_label"
                        local label="Voltage"
                        if [[ -f "$label_file" ]]; then
                            label=$(cat "$label_file" 2>/dev/null || echo "Voltage")
                        fi
                        echo "  $label: ${volt_v}V"
                    fi
                done

                # Check for power sensors
                for power in "$hwmon"/power*_input; do
                    if [[ -f "$power" ]]; then
                        local power_val=$(cat "$power" 2>/dev/null || echo "0")
                        local power_w=$(echo "scale=3; $power_val / 1000000" | bc -l 2>/dev/null || echo "0")
                        local sensor_file=$(basename "$power")
                        local label_file="${power%_input}_label"
                        local label="Power"
                        if [[ -f "$label_file" ]]; then
                            label=$(cat "$label_file" 2>/dev/null || echo "Power")
                        fi
                        echo "  $label ($sensor_file): ${power_w}W"
                        echo "    Path: $power"
                    fi
                done

                # Check for current sensors
                for current in "$hwmon"/curr*_input; do
                    if [[ -f "$current" ]]; then
                        local curr_val=$(cat "$current" 2>/dev/null || echo "0")
                        local curr_a=$(echo "scale=3; $curr_val / 1000" | bc -l 2>/dev/null || echo "0")
                        local label_file="${current%_input}_label"
                        local label="Current"
                        if [[ -f "$label_file" ]]; then
                            label=$(cat "$label_file" 2>/dev/null || echo "Current")
                        fi
                        echo "  $label: ${curr_a}A"
                    fi
                done
                echo
            fi
        done
    fi

    if [[ $found_sensors -eq 0 ]]; then
        print_warning "No hwmon sensors found"
        print_info "This is normal for some ARM systems - checking alternative interfaces..."
    fi
    echo
}

# Function to check thermal zones
scan_thermal_zones() {
    print_header "Thermal Zones"

    local found_thermal=0

    if [[ -d /sys/class/thermal ]]; then
        for zone in /sys/class/thermal/thermal_zone*; do
            if [[ -d "$zone" ]]; then
                found_thermal=1
                local zone_name=$(basename "$zone")
                local temp_file="$zone/temp"
                local type_file="$zone/type"

                if [[ -f "$temp_file" ]]; then
                    local temp=$(cat "$temp_file" 2>/dev/null || echo "0")
                    local temp_c=$((temp / 1000))
                    local zone_type="unknown"

                    if [[ -f "$type_file" ]]; then
                        zone_type=$(cat "$type_file" 2>/dev/null || echo "unknown")
                    fi

                    echo "$zone_name ($zone_type): ${temp_c}°C"

                    # Check for trip points
                    for trip in "$zone"/trip_point_*_temp; do
                        if [[ -f "$trip" ]]; then
                            local trip_temp=$(cat "$trip" 2>/dev/null || echo "0")
                            local trip_temp_c=$((trip_temp / 1000))
                            local trip_name=$(basename "$trip" | sed 's/_temp$//')
                            echo "  $trip_name: ${trip_temp_c}°C"
                        fi
                    done
                fi
            fi
        done
    fi

    if [[ $found_thermal -eq 0 ]]; then
        print_warning "No thermal zones found"
    fi
    echo
}

# Function to check CPU frequency scaling
scan_cpu_frequency() {
    print_header "CPU Frequency Scaling"

    local cpu_count=$(nproc)
    echo "Total CPUs: $cpu_count"

    # Check if cpufreq is available
    if [[ -d /sys/devices/system/cpu/cpu0/cpufreq ]]; then
        local governor_file="/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"
        local min_freq_file="/sys/devices/system/cpu/cpu0/cpufreq/scaling_min_freq"
        local max_freq_file="/sys/devices/system/cpu/cpu0/cpufreq/scaling_max_freq"
        local available_freq_file="/sys/devices/system/cpu/cpu0/cpufreq/scaling_available_frequencies"
        local available_gov_file="/sys/devices/system/cpu/cpu0/cpufreq/scaling_available_governors"

        if [[ -f "$governor_file" ]]; then
            echo "Governor: $(cat "$governor_file" 2>/dev/null || echo "unknown")"
        fi

        if [[ -f "$min_freq_file" ]] && [[ -f "$max_freq_file" ]]; then
            local min_freq=$(cat "$min_freq_file" 2>/dev/null || echo "0")
            local max_freq=$(cat "$max_freq_file" 2>/dev/null || echo "0")
            echo "Frequency Range: $((min_freq / 1000)) - $((max_freq / 1000)) MHz"
        fi

        if [[ -f "$available_gov_file" ]]; then
            echo "Available Governors: $(cat "$available_gov_file" 2>/dev/null || echo "none")"
        fi

        if [[ -f "$available_freq_file" ]]; then
            local freqs=$(cat "$available_freq_file" 2>/dev/null || echo "")
            if [[ -n "$freqs" ]]; then
                echo "Available Frequencies (MHz):"
                for freq in $freqs; do
                    echo "  $((freq / 1000))"
                done
            fi
        fi

        # Show current frequencies for first 8 CPUs
        echo "Current Frequencies:"
        local cpu=0
        while [[ $cpu -lt $cpu_count ]] && [[ $cpu -lt 8 ]]; do
            local cur_freq_file="/sys/devices/system/cpu/cpu$cpu/cpufreq/scaling_cur_freq"
            if [[ -f "$cur_freq_file" ]]; then
                local cur_freq=$(cat "$cur_freq_file" 2>/dev/null || echo "0")
                echo "  CPU$cpu: $((cur_freq / 1000)) MHz"
            fi
            cpu=$((cpu + 1))
        done

        if [[ $cpu_count -gt 8 ]]; then
            echo "  ... (showing first 8 of $cpu_count CPUs)"
        fi
    else
        print_warning "CPU frequency scaling not available"
        print_info "This may indicate fixed frequency operation or virtualized environment"
    fi
    echo
}

# Function to scan for ARM PMU (Performance Monitoring Unit)
scan_arm_pmu() {
    print_header "ARM Performance Monitoring Unit (PMU)"

    # Check for perf capabilities
    if command -v perf >/dev/null 2>&1; then
        print_info "perf tool available - can access hardware performance counters"

        # List available events
        local events=$(perf list 2>/dev/null | grep -E "(cpu|cache|branch|power)" | head -10 | wc -l)
        echo "Available PMU events: $events (sample shown below)"
        perf list 2>/dev/null | grep -E "(cpu|cache|branch)" | head -5 | sed 's/^/  /'

        # Check for power events
        if perf list 2>/dev/null | grep -q power; then
            print_info "Power-related PMU events found:"
            perf list 2>/dev/null | grep power | head -3 | sed 's/^/  /'
        fi
    else
        print_warning "perf tool not available - install linux-tools package for PMU access"
    fi

    # Check for ARM AMU (Activity Monitor Unit)
    if [[ -d /sys/devices/platform ]]; then
        local amu_devices=$(find /sys/devices/platform -name "*amu*" 2>/dev/null | wc -l)
        if [[ $amu_devices -gt 0 ]]; then
            print_info "ARM Activity Monitor Unit (AMU) devices found: $amu_devices"
        fi
    fi

    # Check for ARM DSU PMU (DynamIQ Shared Unit)
    if [[ -d /sys/devices/platform ]]; then
        local dsu_devices=$(find /sys/devices/platform -name "*dsu*" 2>/dev/null | wc -l)
        if [[ $dsu_devices -gt 0 ]]; then
            print_info "ARM DynamIQ Shared Unit (DSU) PMU found: $dsu_devices"
        fi
    fi
    echo
}

# Function to check ACPI power interfaces
scan_acpi_power() {
    print_header "ACPI Power Interfaces"

    # Check for ACPI power supplies
    if [[ -d /sys/class/power_supply ]]; then
        local supplies=$(ls /sys/class/power_supply/ 2>/dev/null | wc -l)
        if [[ $supplies -gt 0 ]]; then
            echo "Power supplies found: $supplies"
            for supply in /sys/class/power_supply/*; do
                local name=$(basename "$supply")
                local type_file="$supply/type"
                local capacity_file="$supply/capacity"
                local power_now_file="$supply/power_now"
                local energy_now_file="$supply/energy_now"

                echo "  $name:"
                if [[ -f "$type_file" ]]; then
                    echo "    Type: $(cat "$type_file" 2>/dev/null || echo "unknown")"
                fi
                if [[ -f "$capacity_file" ]]; then
                    echo "    Capacity: $(cat "$capacity_file" 2>/dev/null || echo "unknown")%"
                fi
                if [[ -f "$power_now_file" ]]; then
                    local power_now=$(cat "$power_now_file" 2>/dev/null || echo "0")
                    local power_w=$(echo "scale=3; $power_now / 1000000" | bc -l 2>/dev/null || echo "0")
                    echo "    Current Power: ${power_w}W"
                fi
                if [[ -f "$energy_now_file" ]]; then
                    local energy_now=$(cat "$energy_now_file" 2>/dev/null || echo "0")
                    local energy_wh=$(echo "scale=3; $energy_now / 1000000" | bc -l 2>/dev/null || echo "0")
                    echo "    Current Energy: ${energy_wh}Wh"
                fi
            done
        else
            print_warning "No ACPI power supplies found"
        fi
    fi
    echo
}

# Function to estimate power based on CPU utilization
estimate_cpu_power() {
    print_header "CPU Power Estimation"

    # Get CPU utilization
    local cpu_idle=$(top -bn1 | grep "Cpu(s)" | awk '{print $8}' | sed 's/%id,//')
    if [[ -n "$cpu_idle" ]]; then
        local cpu_util=$(echo "100 - $cpu_idle" | bc -l 2>/dev/null || echo "0")
        echo "Current CPU Utilization: ${cpu_util}%"

        # Estimate power based on Ampere eMAG specifications
        # Base power consumption + scaling with utilization
        local base_power=15  # Estimated idle power for eMAG
        local max_power=100  # Estimated max power for 32-core eMAG
        local estimated_power=$(echo "scale=2; $base_power + ($max_power - $base_power) * $cpu_util / 100" | bc -l 2>/dev/null || echo "unknown")

        echo "Estimated CPU Power: ${estimated_power}W (based on utilization)"
        print_info "This is a rough estimate - actual power depends on workload characteristics"
    else
        print_warning "Cannot determine CPU utilization"
    fi
    echo
}

# Function to run continuous monitoring
run_continuous_monitor() {
    print_header "Continuous ARM Power Monitoring"
    print_info "Press Ctrl+C to stop monitoring"
    echo

    while true; do
        clear
        echo "ARM Power Monitor - $(date)"
        echo "=================================="

        # Basic system info
        echo "System: $(uname -n) | Uptime: $(uptime -p)"
        echo "Load: $(cat /proc/loadavg | cut -d' ' -f1-3)"
        echo

        # CPU frequencies (first 4 cores)
        echo "CPU Frequencies (first 4 cores):"
        for cpu in {0..3}; do
            local freq_file="/sys/devices/system/cpu/cpu$cpu/cpufreq/scaling_cur_freq"
            if [[ -f "$freq_file" ]]; then
                local freq=$(cat "$freq_file" 2>/dev/null || echo "0")
                echo "  CPU$cpu: $((freq / 1000)) MHz"
            fi
        done
        echo

        # Temperatures
        echo "Temperatures:"
        for zone in /sys/class/thermal/thermal_zone*/temp; do
            if [[ -f "$zone" ]]; then
                local temp=$(cat "$zone" 2>/dev/null || echo "0")
                local temp_c=$((temp / 1000))
                local zone_name=$(basename "$(dirname "$zone")")
                echo "  $zone_name: ${temp_c}°C"
            fi
        done
        echo

        # Power readings
        echo "Power Readings:"
        local found_power=0
        for power in /sys/class/hwmon/*/power*_input; do
            if [[ -f "$power" ]]; then
                local power_val=$(cat "$power" 2>/dev/null || echo "0")
                local power_w=$(echo "scale=3; $power_val / 1000000" | bc -l 2>/dev/null || echo "0")
                local sensor_name=$(basename "$(dirname "$power")")
                local sensor_file=$(basename "$power")

                # Try to get label
                local label_file="${power%_input}_label"
                local label=""
                if [[ -f "$label_file" ]]; then
                    label=$(cat "$label_file" 2>/dev/null || echo "")
                fi

                if [[ -n "$label" ]]; then
                    echo "  $sensor_name/$sensor_file ($label): ${power_w}W"
                else
                    echo "  $sensor_name/$sensor_file: ${power_w}W"
                fi
                echo "    Path: $power"
                found_power=1
            fi
        done

        if [[ $found_power -eq 0 ]]; then
            estimate_cpu_power | grep "Estimated CPU Power" | sed 's/^/  /'
        fi

        echo
        echo "Refresh in 2 seconds..."
        sleep 2
    done
}

# Function to output JSON format
output_json() {
    local timestamp=$(date -Iseconds)
    local hostname=$(hostname)
    local architecture=$(uname -m)
    local cpu_count=$(nproc)

    echo "{"
    echo "  \"timestamp\": \"$timestamp\","
    echo "  \"hostname\": \"$hostname\","
    echo "  \"architecture\": \"$architecture\","
    echo "  \"cpu_count\": $cpu_count,"

    # CPU info
    echo "  \"cpu\": {"
    if [[ -f /proc/cpuinfo ]]; then
        local vendor=$(grep -m1 "CPU implementer" /proc/cpuinfo | cut -d: -f2 | xargs || echo "unknown")
        local model=$(grep -m1 "model name" /proc/cpuinfo | cut -d: -f2 | xargs || echo "unknown")
        echo "    \"vendor_id\": \"$vendor\","
        echo "    \"model\": \"$model\","
    fi

    # Frequencies
    echo "    \"frequencies\": ["
    local cpu=0
    local first=true
    while [[ $cpu -lt $cpu_count ]] && [[ $cpu -lt 8 ]]; do
        local freq_file="/sys/devices/system/cpu/cpu$cpu/cpufreq/scaling_cur_freq"
        if [[ -f "$freq_file" ]]; then
            local freq=$(cat "$freq_file" 2>/dev/null || echo "0")
            if [[ "$first" == "false" ]]; then echo ","; fi
            echo -n "      {\"cpu\": $cpu, \"frequency_mhz\": $((freq / 1000))}"
            first=false
        fi
        cpu=$((cpu + 1))
    done
    echo
    echo "    ]"
    echo "  },"

    # Thermal data
    echo "  \"thermal\": ["
    local first=true
    for zone in /sys/class/thermal/thermal_zone*/temp; do
        if [[ -f "$zone" ]]; then
            local temp=$(cat "$zone" 2>/dev/null || echo "0")
            local temp_c=$((temp / 1000))
            local zone_name=$(basename "$(dirname "$zone")")
            local type_file="$(dirname "$zone")/type"
            local zone_type="unknown"
            if [[ -f "$type_file" ]]; then
                zone_type=$(cat "$type_file" 2>/dev/null || echo "unknown")
            fi

            if [[ "$first" == "false" ]]; then echo ","; fi
            echo "    {"
            echo "      \"zone\": \"$zone_name\","
            echo "      \"type\": \"$zone_type\","
            echo "      \"temperature_celsius\": $temp_c"
            echo -n "    }"
            first=false
        fi
    done
    echo
    echo "  ],"

    # Power data
    echo "  \"power\": ["
    local first=true
    for power in /sys/class/hwmon/*/power*_input; do
        if [[ -f "$power" ]]; then
            local power_val=$(cat "$power" 2>/dev/null || echo "0")
            local power_w=$(echo "scale=3; $power_val / 1000000" | bc -l 2>/dev/null || echo "0")
            local sensor_name=$(basename "$(dirname "$power")")

            if [[ "$first" == "false" ]]; then echo ","; fi
            echo "    {"
            echo "      \"sensor\": \"$sensor_name\","
            echo "      \"power_watts\": $power_w"
            echo -n "    }"
            first=false
        fi
    done
    echo
    echo "  ]"
    echo "}"
}

# Main function
main() {
    echo "$SCRIPT_NAME v$VERSION"
    echo "Optimized for Ampere eMAG and ARM servers"
    echo

    check_privileges
    echo

    case "${1:-scan}" in
        "scan")
            detect_arm_processor
            scan_hwmon_sensors
            scan_thermal_zones
            scan_cpu_frequency
            scan_arm_pmu
            scan_acpi_power
            estimate_cpu_power
            ;;
        "monitor")
            run_continuous_monitor
            ;;
        "json")
            output_json
            ;;
        "help"|"-h"|"--help")
            echo "Usage: $0 [command]"
            echo
            echo "Commands:"
            echo "  scan     - Scan for all available power monitoring interfaces (default)"
            echo "  monitor  - Run continuous power monitoring"
            echo "  json     - Output current readings in JSON format"
            echo "  help     - Show this help message"
            echo
            echo "Examples:"
            echo "  $0                 # Full power capability scan"
            echo "  $0 monitor         # Continuous monitoring"
            echo "  $0 json            # JSON output for integration"
            echo "  sudo $0 scan       # Full scan with root privileges"
            ;;
        *)
            print_error "Unknown command: $1"
            echo "Use '$0 help' for usage information"
            exit 1
            ;;
    esac
}

# Check for required tools
if ! command -v bc >/dev/null 2>&1; then
    print_warning "bc (calculator) not found - some calculations may not work"
    print_info "Install with: apt-get install bc (Ubuntu/Debian) or yum install bc (RHEL/CentOS)"
fi

# Run main function with all arguments
main "$@"