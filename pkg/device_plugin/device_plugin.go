package device_plugin

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	cdihandler "kata-xpu-device-plugin/cdi"

	klog "k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const (
	nvidiaVendorID = "10de"
	cdiConfigPath  = "/var/run/cdi/"
	vfioPath       = "/dev/vfio"
)

// Structure to hold details about Nvidia GPU Device
type NvidiaGpuDevice struct {
	addr  string // PCI address of device
	index uint   // PCI device index on PCI Bus
}

// Key is iommu group id and value is a list of gpu devices part of the iommu group
var iommuMap map[string][]NvidiaGpuDevice

// Keys are the distinct Nvidia GPU device ids present on system and value is the list of all iommu group ids which are of that device id
var deviceMap map[string][]string

var basePath = "/sys/bus/pci/devices"
var pciIdsFilePath = "/usr/pci.ids"
var readLink = readLinkFunc
var readIDFromFile = readIDFromFileFunc
var startDevicePlugin = startDevicePluginFunc

var stop = make(chan struct{})

func InitiateDevicePlugin(strategy string, version string) ([]*GenericDevicePlugin, bool, error) {
	var devicePlugins []*GenericDevicePlugin
	//Identifies GPUs and represents it in appropriate structures
	createIommuDeviceMap()

	// Generate cdi spec for vfio devices
	if err := generateCDISpec(iommuMap, version); err != nil {
		return devicePlugins, true, err
	}

	return createDevicePlugins(strategy)
}

func StopPlugins(devicePlugins []*GenericDevicePlugin) error {
	klog.Infof("[createDevicePlugins] Shutting down device plugin controller")
	for _, v := range devicePlugins {
		if err := v.Stop(); err != nil {
			return err
		}
	}
	return nil
}

func generateCDISpec(iommuMap map[string][]NvidiaGpuDevice, version string) error {
	cs := cdihandler.New(version)
	cs.NewContainerEdits(nil)

	for devName, devices := range iommuMap {
		//devName string, annotations map[string]string, devices []*DeviceNode
		annotations := map[string]string{}
		for _, dev := range devices {
			if version >= cdihandler.CurrentVersion {
				annotations = map[string]string{
					"attach-pci": "true",
				}
				key := fmt.Sprintf("%svfio%v", cdihandler.CdiK8SPrefix, devName)
				value := fmt.Sprintf("%s=%v", cdihandler.DefaultKind, dev.index)
				annotations[key] = value
				annotations["bdf"] = dev.addr
			}
			cdiDevs := []*cdihandler.DeviceNode{}
			node := cdihandler.DeviceNode{
				Path: fmt.Sprintf("%s/%s", vfioPath, devName),
			}
			cdiDevs = append(cdiDevs, &node)
			cs.NewDevice(fmt.Sprintf("%v", dev.index), annotations, cdiDevs)
		}
	}

	if err := cs.Save(cdiConfigPath, "cdi-gpu-vfio", "YAML"); err != nil {
		klog.Infof("[generateCDISpec] save cdi config  %v failed", fmt.Sprintf("%v/cdi-gpu-vfio.yaml", cdiConfigPath))
		return err
	}

	return nil
}

// Starts gpu pass through device plugin
func createDevicePlugins(strategy string) ([]*GenericDevicePlugin, bool, error) {
	var devicePlugins []*GenericDevicePlugin
	var devs []*pluginapi.Device
	// Iommu Map map[214:[{0000:c1:00.0}] 215:[{0000:c5:00.0}] 75:[{0000:3d:00.0}] 76:[{0000:41:00.0}]]
	klog.Infof("[createDevicePlugins] IommuMap %v", iommuMap)
	klog.Infof("[createDevicePlugins] DeviceMap %v", deviceMap)

	//Iterate over deivceMap to create device plugin for each type of GPU on the host
	for k, v := range deviceMap {
		devs = nil
		for _, dev := range v {
			devs = append(devs, &pluginapi.Device{
				ID:     dev,
				Health: pluginapi.Healthy,
			})
		}
		devpluginName := getDeviceName(k)
		if devpluginName == "" {
			klog.Infof("[createDevicePlugins] WARN: Could not find device name for device id: %s", k)
			devpluginName = k
		}
		klog.Infof("[createDevicePlugins] Device Plugin Name %s", devpluginName)
		dp := NewGenericDevicePlugin(devpluginName, vfioPath, devs, strategy)
		err := startDevicePlugin(dp)
		if err != nil {
			klog.Infof("[createDevicePlugins] Error starting %s device plugin: %v", dp.devpluginName, err)
			return devicePlugins, true, err
		} else {
			devicePlugins = append(devicePlugins, dp)
		}
	}

	return devicePlugins, false, nil
}

func startDevicePluginFunc(dp *GenericDevicePlugin) error {
	return dp.Start(stop)
}

// Discovers all Nvidia GPUs which are loaded with VFIO-PCI driver and creates corresponding maps
func createIommuDeviceMap() error {
	iommuMap = make(map[string][]NvidiaGpuDevice)
	deviceMap = make(map[string][]string)
	// pci device index on PCI bus, begin at index=0
	busIndex := uint(0)
	//Walk directory to discover pci devices
	// basePath := /sys/bus/pci/devices
	filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			klog.Infof("[createIommuDeviceMap] Error accessing file path %q: %v\n", path, err)
			return err
		}
		if info.IsDir() {
			klog.Infof("[createIommuDeviceMap] Not a device, continuing")
			return nil
		}
		//Retrieve vendor for the device
		vendorID, err := readIDFromFile(basePath, info.Name(), "vendor")
		if err != nil {
			klog.Infof("[createIommuDeviceMap] Could not get vendor ID for device %v", info.Name())
			return nil
		}

		//Nvidia vendor id is "10de". Proceed if vendor id is 10de
		if vendorID == "10de" {
			//Retrieve iommu group for the device
			driver, err := readLink(basePath, info.Name(), "driver")
			if err != nil {
				klog.Infof("[createIommuDeviceMap] Could not get driver for device %v ", info.Name())
				return nil
			}
			if driver == "vfio-pci" {
				iommuGroup, err := readLink(basePath, info.Name(), "iommu_group")
				if err != nil {
					klog.Infof("[createIommuDeviceMap] Could not get IOMMU Group for device %v ", info.Name())
					return nil
				}
				_, exists := iommuMap[iommuGroup]
				if !exists {
					deviceID, err := readIDFromFile(basePath, info.Name(), "device")
					if err != nil {
						klog.Infof("[createIommuDeviceMap] Could get deviceID with info Name %v", info.Name())
						return nil
					}
					deviceMap[deviceID] = append(deviceMap[deviceID], iommuGroup)
				}
				iommuMap[iommuGroup] = append(iommuMap[iommuGroup], NvidiaGpuDevice{
					addr:  info.Name(),
					index: busIndex,
				})
				busIndex += 1
			}
		}
		return nil
	})

	return nil
}

// Read a file to retrieve ID
func readIDFromFileFunc(basePath string, deviceAddress string, property string) (string, error) {
	data, err := os.ReadFile(filepath.Join(basePath, deviceAddress, property))
	if err != nil {
		klog.Errorf("Could not read %s for device %s: %s", property, deviceAddress, err)
		return "", err
	}
	id := strings.Trim(string(data[2:]), "\n")
	return id, nil
}

// Read a file link
func readLinkFunc(basePath string, deviceAddress string, link string) (string, error) {
	path, err := os.Readlink(filepath.Join(basePath, deviceAddress, link))
	if err != nil {
		klog.Errorf("Could not read link %s for device %s: %s", link, deviceAddress, err)
		return "", err
	}
	_, file := filepath.Split(path)
	return file, nil
}

func getIommuMap() map[string][]NvidiaGpuDevice {
	return iommuMap
}

func getDeviceName(deviceID string) string {
	devpluginName := ""
	file, err := os.Open(pciIdsFilePath)
	if err != nil {
		klog.Infof("Error opening pci ids file %s", pciIdsFilePath)
		return ""
	}
	defer file.Close()

	// Locate beginning of NVIDIA device list in pci.ids file
	scanner, err := locateVendor(file, nvidiaVendorID)
	if err != nil {
		klog.Infof("Error locating NVIDIA in pci.ds file: %v", err)
		return ""
	}

	// Find NVIDIA device by device id
	prefix := fmt.Sprintf("\t%s", deviceID)
	for scanner.Scan() {
		line := scanner.Text()
		// ignore comments
		if strings.HasPrefix(line, "#") {
			continue
		}
		// if line does not start with tab, we are visiting a different vendor
		if !strings.HasPrefix(line, "\t") {
			klog.Infof("Could not find NVIDIA device with id: %s", deviceID)
			return ""
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}

		devpluginName = strings.TrimPrefix(line, prefix)
		devpluginName = strings.TrimSpace(devpluginName)
		devpluginName = strings.ToUpper(devpluginName)
		devpluginName = strings.Replace(devpluginName, "/", "_", -1)
		devpluginName = strings.Replace(devpluginName, ".", "_", -1)
		// Replace all spaces with underscore
		reg, _ := regexp.Compile("\\s+")
		devpluginName = reg.ReplaceAllString(devpluginName, "_")
		// Removes any char other than alphanumeric and underscore
		reg, _ = regexp.Compile("[^a-zA-Z0-9_.]+")
		devpluginName = reg.ReplaceAllString(devpluginName, "")
		break
	}

	if err := scanner.Err(); err != nil {
		klog.Infof("Error reading pci ids file %s", err)
	}
	return devpluginName
}

func locateVendor(pciIdsFile *os.File, vendorID string) (*bufio.Scanner, error) {
	scanner := bufio.NewScanner(pciIdsFile)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, vendorID) {
			return scanner, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return scanner, fmt.Errorf("error reading pci.ids file: %v", err)
	}

	return scanner, fmt.Errorf("failed to find vendor id in pci.ids file: %s", vendorID)
}
