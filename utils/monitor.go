package utils

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jochenvg/go-udev"
	"k8s.io/klog"
)

const (
	VfiDevicePath string = "/dev/vfio"
)

type IOMMUMonitor struct {
	mu          sync.Mutex
	initialDevs map[string]bool
	callback    func(changed bool)
	stopChan    chan struct{}
}

func NewIOMMUMonitor(callback func(changed bool)) *IOMMUMonitor {
	return &IOMMUMonitor{
		initialDevs: make(map[string]bool),
		callback:    callback,
		stopChan:    make(chan struct{}),
	}
}

func (m *IOMMUMonitor) initializeDevices() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries, err := os.ReadDir(VfiDevicePath)
	if err != nil {
		return fmt.Errorf("failed to read /dev/vfio directory: %v", err)
	}

	m.initialDevs = make(map[string]bool)
	for _, entry := range entries {
		m.initialDevs[entry.Name()] = true
	}

	return nil
}

func (m *IOMMUMonitor) watchDeviceChanges() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	currentDevs := make(map[string]bool)
	entries, err := os.ReadDir(VfiDevicePath)
	if err != nil {
		klog.Infof("Error reading /dev/vfio directory: %v", err)
		return false
	}

	for _, entry := range entries {
		currentDevs[entry.Name()] = true
	}

	changed := false
	for dev := range currentDevs {
		if !m.initialDevs[dev] {
			changed = true
			break
		}
	}
	if !changed {
		for dev := range m.initialDevs {
			if !currentDevs[dev] {
				changed = true
				break
			}
		}
	}

	return false
}

func (m *IOMMUMonitor) StartMonitoring() error {
	// Initialize devices
	if err := m.initializeDevices(); err != nil {
		return err
	}

	// setup udev client
	u := udev.Udev{}
	monitor := u.NewMonitorFromNetlink("udev")

	// Add Filter
	if err := monitor.FilterAddMatchSubsystem("vfio"); err != nil {
		return fmt.Errorf("failed to add subsystem filter: %v", err)
	}

	// Set up udev monitoring
	// Create a context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Start monitor goroutine and get receive channel
	eventChan, errChan, _ := monitor.DeviceChan(ctx)
	klog.Infoln("VFIO Device Monitor Start Now!")

	// handle udev Event
	for {
		select {
		case <-m.stopChan:
			m.Stop(cancel)
			return nil

		case dev, ok := <-eventChan:
			if !ok {
				klog.Infof("VFIO Device Changes Watched skip.")
				return nil
			}
			if dev.Subsystem() == "vfio" && strings.HasPrefix(dev.Devpath(), "/dev/vfio/") {
				klog.Infof("VFIO Device Changes [%s]: %s Watched.",
					dev.Action(),
					dev.Devpath())

				if changed := m.watchDeviceChanges(); changed {
					klog.Infoln("IOMMU Group updates")
					if m.callback != nil {
						m.callback(true)
					}
				}
			}

		case err, ok := <-errChan:
			if !ok { // Channel Closed
				return nil
			}
			if errors.Is(err, context.Canceled) {
				klog.Infoln("Monitor Stopped expectedly")
				return nil
			}
			klog.Infof("Monitor failed: %v", err)
			// Auto start monitor
			go m.StartMonitoring()
			return nil
		}
	}
}

func (m *IOMMUMonitor) Stop(cancel context.CancelFunc) {
	close(m.stopChan)
	cancel()
}
