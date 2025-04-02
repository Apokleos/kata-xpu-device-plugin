package main

import (
	"fmt"
	"kata-xpu-device-plugin/utils"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"kata-xpu-device-plugin/pkg/device_plugin"
	dp "kata-xpu-device-plugin/pkg/device_plugin"

	"github.com/fsnotify/fsnotify"
	"github.com/urfave/cli/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"

	"k8s.io/klog/v2"
)

type options struct {
	flags              []cli.Flag
	kubeletSocket      string
	VfioDevicePath     string
	DeviceListStrategy string
}

func main() {
	c := cli.NewApp()
	o := &options{}
	o.VfioDevicePath = utils.VfioDevicePath
	c.Name = "Kata GPU Device Plugin"
	c.Usage = "Kata GPU device plugin for Kubernetes"
	c.Version = "V1.0.0"
	c.Action = func(ctx *cli.Context) error {
		return start(ctx, o)
	}

	c.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:    "fail-on-init-error",
			Value:   true,
			Usage:   "fail the plugin if an error is encountered during initialization, otherwise block indefinitely",
			EnvVars: []string{"FAIL_ON_INIT_ERROR"},
		},
		&cli.StringFlag{
			Name:        "kubelet-socket",
			Value:       pluginapi.KubeletSocket,
			Usage:       "specify the socket for communicating with the kubelet; if this is empty, no connection with the kubelet is attempted",
			Destination: &o.kubeletSocket,
			EnvVars:     []string{"KUBELET_SOCKET"},
		},
		&cli.StringFlag{
			Name:    "device-list-strategy",
			Value:   string(dp.DefaultDeviceListStrategy),
			Usage:   "the desired strategy for passing the device list to the underlying runtime:\n\t\t< cdi-cri | cdi-annotations >",
			EnvVars: []string{"DEVICE_LIST_STRATEGY"},
		},
	}
	o.flags = c.Flags

	err := c.Run(os.Args)
	if err != nil {
		klog.Error(err)
		os.Exit(1)
	}
}

func startPlugin(strategy string) ([]*dp.GenericDevicePlugin, bool, error) {
	klog.Infof("start plugin with strategy %v", strategy)
	return device_plugin.InitiateDevicePlugin(strategy, "0.5.0")
}

func startAndMonitor(c *cli.Context, o *options) ([]*dp.GenericDevicePlugin, bool, error) {
	var devicePlugins []*dp.GenericDevicePlugin
	monitor := utils.NewIOMMUMonitor(func(changed bool) {
		if changed {
			klog.Infoln("Triggering daemonset restart due to IOMMU group changes")

			// Calling a restart function
			_, _, err := startPlugin(o.DeviceListStrategy)
			if err != nil {
				klog.Fatalf("error start plugins: %v", err)
			}
		}
	})

	klog.InfoS(fmt.Sprintf("Starting %s", c.App.Name), "version", c.App.Version)

	devicePlugins, _, err := startPlugin(o.DeviceListStrategy)
	if err != nil {
		err = dp.StopPlugins(devicePlugins)
		if err != nil {
			return devicePlugins, true, fmt.Errorf("error stopping plugins: %v", err)
		}
	}

	go func() {
		if err := monitor.StartMonitoring(); err != nil {
			klog.Infof("Monitoring %v", o.VfioDevicePath)
			err = dp.StopPlugins(devicePlugins)
			if err != nil {
				klog.Errorf("error stopping plugins: %v", err)
			}
		}
	}()

	return devicePlugins, false, nil
}

func start(c *cli.Context, o *options) error {
	if o.DeviceListStrategy == "" {
		o.DeviceListStrategy = dp.DefaultDeviceListStrategy
	}
	klog.InfoS(fmt.Sprintf("Starting %s", c.App.Name), "version", c.App.Version, "Strategy", o.DeviceListStrategy)

	kubeletSocketDir := filepath.Dir(o.kubeletSocket)
	klog.Infof("Starting FS watcher for %v", kubeletSocketDir)
	watcher, err := utils.Files(kubeletSocketDir)
	if err != nil {
		return fmt.Errorf("failed to create FS watcher for %s: %v", pluginapi.DevicePluginPath, err)
	}
	defer watcher.Close()

	klog.Info("Starting OS watcher.")
	sigs := utils.Signals(syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	var started bool
	var restartTimeout <-chan time.Time
	var plugins []*dp.GenericDevicePlugin
restart:
	// If we are restarting, stop plugins from previous run.
	if started {
		err := dp.StopPlugins(plugins)
		if err != nil {
			return fmt.Errorf("error stopping plugins from previous run: %v", err)
		}
	}

	klog.Info("Starting Plugins.")
	plugins, restartPlugins, err := startAndMonitor(c, o)
	if err != nil {
		return fmt.Errorf("error starting plugins: %v", err)
	}
	started = true

	if restartPlugins {
		klog.Infof("Failed to start one or more plugins. Retrying in 30s...")
		restartTimeout = time.After(30 * time.Second)
	}
	doMonitorWithPolling(o.DeviceListStrategy)
	// Start an infinite loop, waiting for several indicators to either log
	// some messages, trigger a restart of the plugins, or exit the program.
	for {
		select {
		// If the restart timeout has expired, then restart the plugins
		case <-restartTimeout:
			goto restart

		// Detect a kubelet restart by watching for a newly created
		// 'pluginapi.KubeletSocket' file. When this occurs, restart this loop,
		// restarting all of the plugins in the process.
		case event := <-watcher.Events:
			if o.kubeletSocket != "" && event.Name == o.kubeletSocket && event.Op&fsnotify.Create == fsnotify.Create {
				klog.Infof("inotify: %s created, restarting.", o.kubeletSocket)
				goto restart
			}

		// Watch for any other fs errors and log them.
		case err := <-watcher.Errors:
			klog.Infof("inotify: %s", err)

		// Watch for any signals from the OS. On SIGHUP, restart this loop,
		// restarting all of the plugins in the process. On all other
		// signals, exit the loop and exit the program.
		case s := <-sigs:
			switch s {
			case syscall.SIGHUP:
				klog.Info("Received SIGHUP, restarting.")
				goto restart
			default:
				klog.Infof("Received signal \"%v\", shutting down.", s)
				goto exit
			}
		}
	}
exit:
	err = dp.StopPlugins(plugins)
	if err != nil {
		return fmt.Errorf("error stopping plugins: %v", err)
	}
	return nil
}

func handleIOMMUGroupChanges(added, removed []string, strategy string) {
	klog.Infof("VFIO group changes detected - Added: %v, Removed: %v", added, removed)
	utils.UpdateKnownGroups(added, removed)

	if len(added) > 0 {
		klog.Infof("New VFIO groups detected: %v", added)
		_, _, err := startPlugin(strategy)
		if err != nil {
			klog.Fatalf("error start plugins: %v", err)
		}
	}
	if len(removed) > 0 {
		klog.Infof("Removed VFIO groups detected: %v", removed)
		_, _, err := startPlugin(strategy)
		if err != nil {
			klog.Fatalf("error start plugins: %v", err)
		}
	}
}

func monitorWithPolling(strategy string) {
	klog.Infoln("Starting periodic VFIO IOMMU Group monitoring...")

	ticker := time.NewTicker(utils.CheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		added, removed, err := utils.CheckCurrentGroups()
		if err != nil {
			klog.Infof("Error checking VFIO groups: %v", err)
			continue
		}

		if len(added)+len(removed) > 0 {
			handleIOMMUGroupChanges(added, removed, strategy)
		}
	}
}

func doMonitorWithPolling(strategy string) {
	klog.Infoln("Starting VFIO device monitor polling service...")

	if err := utils.InitializeGroups(); err != nil {
		klog.Fatalf("Initialization failed: %v", err)
	}

	go monitorWithPolling(strategy)
}
