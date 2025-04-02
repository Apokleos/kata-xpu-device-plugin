#!/bin/bash
set -eo pipefail

# date statistic
START_TIME=$(date +%s.%N)
declare -g SCRIPT_DURATION=0

# Constant definitions
LOG_PREFIX="[NVIDIA GPU CDI Setup]"
VERSION="1.3.0"
DEFAULT_CDI_SPEC="/var/run/cdi/nvidia-gpu-cdi.yaml"

# Default configuration (overridable by arguments)
# nvidia-drm.ko  nvidia-modeset.ko  nvidia-peermem.ko  nvidia-uvm.ko  nvidia.ko
REQUIRED_MODULES=("nvidia" "nvidia_uvm" "nvidia_modeset") # Kernel modules to check and load
MIN_DRIVER_VERSION=535                                    # Default minimum driver version
CDI_SPEC_DIR="/var/run/cdi/"
CDI_SPEC=${DEFAULT_CDI_SPEC} # Default output path
# DEVICE_PATHS=("/dev/nvidiactl" "/dev/nvidia-uvm", "/dev/nvidia-modeset") # Critical device paths
DEVICE_PATHS=("/dev/nvidiactl") # Critical device paths
DEVICE_PREFIX="/dev/nvidia"
CONTROL_DEVICES=("ctl" "uvm" "modeset")

# Runtime flags
DEBUG=0
SKIP_SMI_CHECK=0
SHOW_HELP=0
SILENT_MODE=0

# Logging function with silent mode support
log() {
    local level=$1
    shift
    local message=$@

    # Silent mode handling
    if [[ ${SILENT_MODE} -eq 1 && ${level} != "ERROR" ]]; then
        return
    fi

    echo "$(date +'%Y-%m-%d %T') ${LOG_PREFIX} [${level}] ${message}" >&2
}

# Display enhanced usage information
usage() {
    cat <<EOF
NVIDIA GPU CDI Configuration Tool v${VERSION}

Usage: $0 [OPTIONS]

Options:
  --skip-smi-check        Skip NVIDIA-SMI validation checks
  --modules=MODULES       Comma-separated list of kernel modules to load (default: nvidia,nvidia_uvm)
  --min-driver-version=N  Minimum required driver version (default: 535)
  --output=PATH           CDI specification output path (default: ${DEFAULT_CDI_SPEC})
  --quiet                 Suppress non-error output
  --version               Show version information
  --help                  Show this help message

Examples:
  $0 --modules=nvidia --output=/etc/cdi/custom.yaml
  $0 --min-driver-version=525 --quiet
EOF
}

# Parse command-line arguments
parse_arguments() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
        --skip-smi-check)
            SKIP_SMI_CHECK=1
            shift
            ;;
        --modules=*)
            IFS=',' read -ra REQUIRED_MODULES <<<"${1#*=}"
            shift
            ;;
        --min-driver-version=*)
            MIN_DRIVER_VERSION="${1#*=}"
            if ! [[ ${MIN_DRIVER_VERSION} =~ ^[0-9]+$ ]]; then
                log ERROR "Invalid driver version format: ${MIN_DRIVER_VERSION}"
                exit 2
            fi
            shift
            ;;
        --output=*)
            CDI_SPEC="${1#*=}"
            shift
            ;;
        --quiet)
            SILENT_MODE=1
            shift
            ;;
        --version)
            echo "NVIDIA CDI Configurator v${VERSION}"
            exit 0
            ;;
        --help | -h)
            SHOW_HELP=1
            shift
            ;;
        *)
            log ERROR "Invalid option: $1"
            usage
            exit 2
            ;;
        esac
    done
}

# calculate and format
calculate_duration() {
    local end_time=$(date +%s.%N)
    SCRIPT_DURATION=$(echo "$end_time - $START_TIME" | bc -l)

    local hours=$(echo "$SCRIPT_DURATION/3600" | bc)
    local remaining=$(echo "$SCRIPT_DURATION%3600" | bc)
    local minutes=$(echo "$remaining/60" | bc)
    local seconds=$(echo "$remaining%60" | bc)

    # format ms part
    local milliseconds=$(echo "($seconds - ${seconds%.*}) * 1000" | bc | cut -d. -f1)
    seconds=${seconds%.*}

    # readable string
    local duration_str=""
    [[ $hours -gt 0 ]] && duration_str+="${hours}h "
    [[ $minutes -gt 0 || $hours -gt 0 ]] && duration_str+="${minutes}m "
    duration_str+="${seconds}s ${milliseconds}ms"

    echo "$duration_str"
}

exit_handler() {
    local exit_status=$?
    local duration=$(calculate_duration)

    # Dump different message based on the exit code
    if [[ $exit_status -eq 0 ]]; then
        log SUCCESS "Script executed successfully"
        [[ $SILENT_MODE -eq 0 ]] && echo -e "\nTotal execution time: $duration"
    else
        log ERROR "Script terminated with error (Exit code: $exit_status)"
        echo -e "\nExecution time before failure: $duration" >&2
    fi

    # Debug mode extra information
    if [[ $DEBUG -eq 1 ]]; then
        log DEBUG "Detailed timings:"
        log DEBUG "  - Real time: $(printf "%.3f" $SCRIPT_DURATION)s"
        log DEBUG "  - User CPU: $(printf "%0.3f" $(ps -o utime= -p $$ | awk '{print $1}'))s"
        log DEBUG "  - System CPU: $(printf "%0.3f" $(ps -o stime= -p $$ | awk '{print $1}'))s"
    fi

    exit $exit_status
}

# Register exit handler
trap exit_handler EXIT INT TERM

validate_file_path() {
    local path=$1
    local dir=$(dirname "${path}")

    # Check directory writability
    if [[ ! -w "${dir}" ]]; then
        log ERROR "Output directory not writable: ${dir}"
        log ACTION "Try: sudo mkdir -p ${dir} && sudo chmod 755 ${dir}"
        return 1
    fi

    # Prevent overwriting special files
    if [[ -e "${path}" && ! -f "${path}" ]]; then
        log ERROR "Output path exists and is not a regular file: ${path}"
        return 1
    fi
}

# Check and load required kernel modules
check_load_kernel_modules() {
    log INFO "Checking required modules: ${REQUIRED_MODULES[*]}"

    for module in "${REQUIRED_MODULES[@]}"; do
        # Check if module is already loaded
        if grep -q "^${module} " /proc/modules; then
            ((DEBUG)) && log DEBUG "Module ${module} already loaded"
            continue
        fi

        # Attempt to load module
        if ! load_kernel_module "${module}"; then
            log ERROR "Critical failure loading ${module}"
            log ACTION "1. Verify NVIDIA driver installation"
            log ACTION "2. Check dmesg output: sudo dmesg | grep -i nvidia"
            log ACTION "3. Try manual load: sudo modprobe ${module}"
            return 1
        fi
    done
}

# Load kernel module with validation
load_kernel_module() {
    local module=$1
    local attempt=0
    local max_attempts=3

    log INFO "Loading kernel module: ${module}"

    # Attempt module load
    until [ $attempt -ge $max_attempts ]; do
        ((attempt++))
        if modprobe "${module}" 2>/dev/null; then
            # Verify module actually loaded
            if grep -q "^${module} " /proc/modules; then
                log INFO "Successfully loaded module: ${module} (attempt ${attempt})"
                return 0
            fi
        fi
        sleep 1
    done

    log ERROR "Failed to load module: ${module} after ${max_attempts} attempts"
    return 1
}

# get major number
get_major_number() {
    local major
    # nvidia major
    major=$(grep nvidia /proc/devices | awk 'NR==1{print $1}')

    if [[ -z "$major" ]]; then
        log ERROR "Cannot determine NVIDIA major device number"
        exit 1
    fi
    echo "$major"
}

# detect gpu counts
detect_gpu_count() {
    local count=0
    # all nvidia gpu device ids
    # local device_ids=(
    #     10de: # Standard GPU
    #     1b06: # GRID
    #     1db6: # Tesla T4
    #     1eb8: # A100
    #     2233: # RTX 3090
    # )

    # for id in "${device_ids[@]}"; do
    #     count=$((count + $(lspci -d "$id" -k 2>/dev/null | grep -E "3D controller|VGA compatible controller")))
    # done

    count=$(lspci -d 10de: -k | grep -E "3D controller|VGA compatible controller" | wc -l)

    # not less than one
    [[ $count -gt 0 ]] || count=1

    echo "$count"
}

# create node device
create_devices() {
    local major=$1
    local gpu_count=$2
    local retries=3

    # create nvidia control devices
    declare -A control_devices=(
        ["ctl"]=255
        ["-uvm"]=254
        ["-modeset"]=253
    )

    for dev in "${!control_devices[@]}"; do
        local path="${DEVICE_PREFIX}${dev}"
        local minor=${control_devices[$dev]}

        log INFO "Creating control device: $path"
        for ((i = 1; i <= retries; i++)); do
            if mknod -m 666 "$path" c "$major" "$minor"; then
                break
            else
                if [[ $i -eq $retries ]]; then
                    log ERROR "Failed to create $path after $retries attempts"
                    exit 1
                fi
                sleep 1
            fi
        done
    done

    # create nvidia node
    for ((i = 0; i < gpu_count; i++)); do
        local path="${DEVICE_PREFIX}${i}"
        log INFO "Creating GPU device: $path"
        if ! mknod -m 666 "$path" c "$major" "$i"; then
            log ERROR "Failed to create $path"
            exit 1
        fi
    done
}

# mknode nvidia devices
mknode_nvidia_devices() {
    [[ ${SKIP_SMI_CHECK} -eq 1 ]] && return 0

    local major=$(get_major_number)
    local gpu_count=$(detect_gpu_count)

    log INFO "Detected NVIDIA devices:"
    log INFO " - Major number: $major"
    log INFO " - GPU count: $gpu_count"

    create_devices "$major" "$gpu_count"

    # dump nvidia devices
    log INFO "Created devices:"
    ls -l ${DEVICE_PREFIX}*
}

check_nvidia_smi() {
    [[ ${SKIP_SMI_CHECK} -eq 0 ]] && return 0

    log INFO "Validating NVIDIA-SMI"

    if ! command -v nvidia-smi &>/dev/null; then
        log ERROR "nvidia-smi not found in PATH"
        log ACTION "Verify NVIDIA driver installation"
        return 1
    fi

    local driver_version full_version
    full_version=$(nvidia-smi --query-gpu=driver_version --format=csv,noheader,nounits | head -1)
    driver_version="${full_version%%.*}"

    if ((driver_version < MIN_DRIVER_VERSION)); then
        log ERROR "Driver version mismatch (Installed: ${full_version}, Required: >=${MIN_DRIVER_VERSION}.xx)"
        return 1
    fi

    if ! nvidia-smi &>/dev/null; then
        log ERROR "nvidia-smi execution failed"
        log ACTION "Check GPU status with: nvidia-smi"
        return 1
    fi
}

generate_cdi_config() {
    log INFO "Generating CDI specification"

    local output_dir=$(dirname "${CDI_SPEC}")
    mkdir -p "${output_dir}" || {
        log ERROR "Failed to create output directory: ${output_dir}"
        return 1
    }

    validate_file_path "${CDI_SPEC}" || return 1

    if ! nvidia-ctk cdi generate \
        --output "${CDI_SPEC}" \
        --format yaml; then
        log ERROR "CDI generation failed"
        return 1
    fi

    [[ -s "${CDI_SPEC}" ]] || {
        log ERROR "Generated empty CDI file"
        return 1
    }

    log INFO "CDI specification created: ${CDI_SPEC}"
}

main() {
    parse_arguments "$@"

    [[ ${SHOW_HELP} -eq 1 ]] && {
        usage
        exit 0
    }

    log INFO "Initializing NVIDIA CDI Setup v${VERSION}"
    check_load_kernel_modules || exit 1
    mknode_nvidia_devices || exit 1
    check_nvidia_smi || exit 1
    generate_cdi_config || exit 1
    log SUCCESS "Configuration completed successfully"
    [[ ${SILENT_MODE} -eq 0 ]] && echo "Output generated: ${CDI_SPEC}"
}

main "$@"
