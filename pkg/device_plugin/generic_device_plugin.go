package device_plugin

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	cdiutils "kata-xpu-device-plugin/cdi"

	"github.com/google/uuid"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
)

const (
	DevicePluginNamespace = "nvidia.com"
	connectionTimeout     = 5 * time.Second
	vfioDevicePath        = "/dev/vfio"
	gpuPrefix             = "PCI_RESOURCE_NVIDIA_COM"
	K8SCDIVendorClass     = "KUBERNETES_CDI_VENDOR_CLASS"
	CdiVendorClass        = "nvidia.com/gpu"
)

var returnIommuMap = getIommuMap

// Implements the kubernetes device plugin API
type GenericDevicePlugin struct {
	devs                 []*pluginapi.Device
	server               *grpc.Server
	socketPath           string
	stop                 chan struct{} // this channel signals to stop the DP
	term                 chan bool     // this channel detects kubelet restarts
	healthy              chan string
	unhealthy            chan string
	devicePath           string
	devpluginName        string
	devsHealth           []*pluginapi.Device
	cdiAnnotationPrefix  string
	deviceListStrategies DeviceListStrategies
}

// DeviceListStrategies defines which strategies are enabled and should
// be used when passing the device list to the container runtime.
type DeviceListStrategies map[string]bool

// NewDeviceListStrategies constructs a new DeviceListStrategy
// func NewDeviceListStrategies(strategies []string) (DeviceListStrategies, error) {
func newDeviceListStrategies() DeviceListStrategies {
	ret := map[string]bool{
		cdiutils.DeviceListStrategyCDIAnnotations: false,
		cdiutils.DeviceListStrategyCDICRI:         true,
	}

	// return DeviceListStrategies(ret), nil
	return DeviceListStrategies(ret)
}

// Includes returns whether the given strategy is present in the set of strategies.
func (s DeviceListStrategies) Includes(strategy string) bool {
	return s[strategy]
}

// Returns an initialized instance of GenericDevicePlugin
func NewGenericDevicePlugin(devpluginName string, devicePath string, devices []*pluginapi.Device) *GenericDevicePlugin {
	log.Println("DevicePlugin Name " + devpluginName)
	serverSock := fmt.Sprintf(pluginapi.DevicePluginPath+"kata-xpu-%s.sock", devpluginName)
	dpi := &GenericDevicePlugin{
		devs:                 devices,
		socketPath:           serverSock,
		term:                 make(chan bool, 1),
		healthy:              make(chan string),
		unhealthy:            make(chan string),
		devpluginName:        devpluginName,
		devicePath:           devicePath,
		deviceListStrategies: newDeviceListStrategies(),
	}
	return dpi
}

func buildEnv(envList map[string][]string) map[string]string {
	env := map[string]string{}
	for key, devList := range envList {
		env[key] = strings.Join(devList, ",")
	}
	return env
}

func waitForGrpcServer(socketPath string, timeout time.Duration) error {
	conn, err := connect(socketPath, timeout)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// dial establishes the gRPC communication with the registered device plugin.
func connect(socketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	ctx, _ := context.WithTimeout(context.Background(), timeout)
	c, err := grpc.DialContext(ctx, socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			if deadline, ok := ctx.Deadline(); ok {
				return net.DialTimeout("unix", addr, time.Until(deadline))
			}
			return net.DialTimeout("unix", addr, connectionTimeout)
		}),
	)
	if err != nil {
		return nil, err
	}

	return c, nil
}

// Start starts the gRPC server of the device plugin
func (dpi *GenericDevicePlugin) Start(stop chan struct{}) error {
	if dpi.server != nil {
		return fmt.Errorf("gRPC server already started")
	}

	dpi.stop = stop

	err := dpi.cleanup()
	if err != nil {
		return err
	}

	sock, err := net.Listen("unix", dpi.socketPath)
	if err != nil {
		log.Printf("[%s] Error creating GRPC server socket: %v", dpi.devpluginName, err)
		return err
	}

	dpi.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(dpi.server, dpi)

	go dpi.server.Serve(sock)

	err = waitForGrpcServer(dpi.socketPath, connectionTimeout)
	if err != nil {
		// this err is returned at the end of the Start function
		log.Printf("[%s] Error connecting to GRPC server: %v", dpi.devpluginName, err)
	}

	err = dpi.Register()
	if err != nil {
		log.Printf("[%s] Error registering with device plugin manager: %v", dpi.devpluginName, err)
		return err
	}

	go dpi.healthCheck()

	log.Println(dpi.devpluginName + " Device plugin server ready")

	return err
}

// Stop stops the gRPC server
func (dpi *GenericDevicePlugin) Stop() error {
	if dpi.server == nil {
		return nil
	}

	// Send terminate signal to ListAndWatch()
	dpi.term <- true

	dpi.server.Stop()
	dpi.server = nil

	return dpi.cleanup()
}

// Restarts DP server
func (dpi *GenericDevicePlugin) restart() error {
	log.Printf("Restarting %s device plugin server", dpi.devpluginName)
	if dpi.server == nil {
		return fmt.Errorf("grpc server instance not found for %s", dpi.devpluginName)
	}

	dpi.Stop()

	// Create new instance of a grpc server
	var stop = make(chan struct{})
	return dpi.Start(stop)
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (dpi *GenericDevicePlugin) Register() error {
	conn, err := connect(pluginapi.KubeletSocket, connectionTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	reqt := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(dpi.socketPath),
		ResourceName: fmt.Sprintf("%s/%s", DevicePluginNamespace, dpi.devpluginName),
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return err
	}
	return nil
}

// ListAndWatch lists devices and update that list according to the health status
func (dpi *GenericDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {

	s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})

	for {
		select {
		case unhealthy := <-dpi.unhealthy:
			log.Printf("In watch unhealthy")
			for _, dev := range dpi.devs {
				if unhealthy == dev.ID {
					dev.Health = pluginapi.Unhealthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})
		case healthy := <-dpi.healthy:
			log.Printf("In watch healthy")
			for _, dev := range dpi.devs {
				if healthy == dev.ID {
					dev.Health = pluginapi.Healthy
				}
			}
			s.Send(&pluginapi.ListAndWatchResponse{Devices: dpi.devs})
		case <-dpi.stop:
			return nil
		case <-dpi.term:
			return nil
		}
	}
}

func (plugin *GenericDevicePlugin) getCDIDeviceAnnotations(id string, devices ...string) (map[string]string, error) {
	annotations, err := cdiapi.UpdateAnnotations(map[string]string{}, "kata-xpu-device-plugin", id, devices)
	if err != nil {
		return nil, fmt.Errorf("failed to add CDI annotations: %v", err)
	}

	if plugin.cdiAnnotationPrefix == cdiutils.DefaultCDIAnnotationPrefix {
		return annotations, nil
	}

	// update annotations if a custom CDI prefix is configured
	updatedAnnotations := make(map[string]string)
	for k, v := range annotations {
		newKey := plugin.cdiAnnotationPrefix + strings.TrimPrefix(k, cdiutils.DefaultCDIAnnotationPrefix)
		updatedAnnotations[newKey] = v
	}

	return updatedAnnotations, nil
}

// updateResponseForCDI updates the specified response for the given device IDs.
// This response contains the annotations required to trigger CDI injection in the container engine or nvidia-container-runtime.
func (plugin *GenericDevicePlugin) updateResponseForCDI(response *pluginapi.ContainerAllocateResponse, responseID string, deviceIDs ...uint) error {
	var devices []string
	for _, id := range deviceIDs {
		devices = append(devices, cdiutils.QualifiedName("nvidia.com", "gpu", fmt.Sprintf("%v", id)))
	}

	if len(devices) == 0 {
		log.Println("devices empty.")
		return nil
	}

	if plugin.deviceListStrategies.Includes(cdiutils.DeviceListStrategyCDIAnnotations) {
		annotations, err := plugin.getCDIDeviceAnnotations(responseID, devices...)
		if err != nil {
			return err
		}
		response.Annotations = annotations
	}
	if plugin.deviceListStrategies.Includes(cdiutils.DeviceListStrategyCDICRI) {
		for _, device := range devices {
			cdiDevice := pluginapi.CDIDevice{
				Name: device,
			}
			response.CDIDevices = append(response.CDIDevices, &cdiDevice)
		}
	}

	return nil
}

func (plugin *GenericDevicePlugin) getAllocateResponse(deviceIDs []uint) (*pluginapi.ContainerAllocateResponse, error) {
	// Create an empty response that will be updated as required below.
	response := &pluginapi.ContainerAllocateResponse{
		Envs: make(map[string]string),
	}

	// 120c8e49-a128-4186-bdbb-af37586bd602
	responseID := uuid.New().String()
	if err := plugin.updateResponseForCDI(response, responseID, deviceIDs...); err != nil {
		return nil, fmt.Errorf("failed to get allocate response for CDI: %v", err)
	}

	return response, nil
}

// Performs pre allocation checks and allocates a devices based on the request
func (dpi *GenericDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	responses := pluginapi.AllocateResponse{}
	for _, req := range reqs.ContainerRequests {
		devIndexes := []uint{}
		for _, iommuId := range req.DevicesIDs {
			returnedMap := returnIommuMap()
			//Retrieve the devices associated with a Iommu group
			nvDevs := returnedMap[iommuId]
			for _, dev := range nvDevs {
				iommuGroup, err := readLink(basePath, dev.addr, "iommu_group")
				if err != nil || iommuGroup != iommuId {
					log.Println("IommuGroup has changed on the system ", dev.addr)
					return nil, fmt.Errorf("invalid allocation request: unknown device: %s", dev.addr)
				}
				vendorID, err := readIDFromFile(basePath, dev.addr, "vendor")
				if err != nil || vendorID != "10de" {
					log.Println("Vendor has changed on the system ", dev.addr)
					return nil, fmt.Errorf("invalid allocation request: unknown device: %s", dev.addr)
				}

				devIndexes = append(devIndexes, dev.index)
			}
		}

		allocated_response, err := dpi.getAllocateResponse(devIndexes)
		if err != nil {
			return nil, fmt.Errorf("failed to get allocate response: %v", err)
		}
		allocated_response.Envs = map[string]string{
			K8SCDIVendorClass: CdiVendorClass,
		}
		responses.ContainerResponses = append(responses.ContainerResponses, allocated_response)
	}

	return &responses, nil
}

func (dpi *GenericDevicePlugin) cleanup() error {
	if err := os.Remove(dpi.socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

func (dpi *GenericDevicePlugin) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	options := &pluginapi.DevicePluginOptions{
		PreStartRequired: false,
	}
	return options, nil
}

func (dpi *GenericDevicePlugin) PreStartContainer(ctx context.Context, in *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	res := &pluginapi.PreStartContainerResponse{}
	return res, nil
}

// GetPreferredAllocation is for compatible with new DevicePluginServer API for DevicePlugin service. It has not been implemented in kubevrit-gpu-device-plugin
func (dpi *GenericDevicePlugin) GetPreferredAllocation(ctx context.Context, in *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	// TODO
	// returns a preferred set of devices to allocate
	// from a list of available ones. The resulting preferred allocation is not
	// guaranteed to be the allocation ultimately performed by the
	// devicemanager. It is only designed to help the devicemanager make a more
	// informed allocation decision when possible.
	return nil, nil
}

// Health check of GPU devices
func (dpi *GenericDevicePlugin) healthCheck() error {
	method := fmt.Sprintf("healthCheck(%s)", dpi.devpluginName)
	log.Printf("%s: invoked", method)
	var pathDeviceMap = make(map[string]string)
	var path = dpi.devicePath
	var health = ""

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("%s: Unable to create fsnotify watcher: %v", method, err)
		return err
	}
	defer watcher.Close()

	err = watcher.Add(filepath.Dir(dpi.socketPath))
	if err != nil {
		log.Printf("%s: Unable to add device plugin socket path to fsnotify watcher: %v", method, err)
		return err
	}

	_, err = os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("%s: Unable to stat device: %v", method, err)
			return err
		}
	}

	for _, dev := range dpi.devs {
		devicePath := filepath.Join(path, dev.ID)
		err = watcher.Add(devicePath)
		log.Printf(" Adding Watcher to Path : %v", devicePath)
		pathDeviceMap[devicePath] = dev.ID
		if err != nil {
			log.Printf("%s: Unable to add device path to fsnotify watcher: %v", method, err)
			return err
		}
	}

	for {
		select {
		case <-dpi.stop:
			return nil
		case event := <-watcher.Events:
			v, ok := pathDeviceMap[event.Name]
			if ok {
				// Health in this case is if the device path actually exists
				if event.Op == fsnotify.Create {
					health = v
					dpi.healthy <- health
				} else if (event.Op == fsnotify.Remove) || (event.Op == fsnotify.Rename) {
					log.Printf("%s: Marking device unhealthy: %s", method, event.Name)
					health = v
					dpi.unhealthy <- health
				}
			} else if event.Name == dpi.socketPath && event.Op == fsnotify.Remove {
				// Watcher event for removal of socket file
				log.Printf("%s: Socket path for GPU device was removed, kubelet likely restarted", method)
				// Trigger restart of the DP servers
				if err := dpi.restart(); err != nil {
					log.Printf("%s: Unable to restart server %v", method, err)
					return err
				}
				log.Printf("%s: Successfully restarted %s device plugin server. Terminating.", method, dpi.devpluginName)
				return nil
			}
		}
	}
}
