package utils

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Files creates a Watcher for the specified files.
func Files(files ...string) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		err = watcher.Add(f)
		if err != nil {
			watcher.Close()
			return nil, err
		}
	}

	return watcher, nil
}

// Signals creats a channel for the specified signals.
func Signals(sigs ...os.Signal) chan os.Signal {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, sigs...)

	return sigChan
}

const (
	// Configuration parameters
	VfioDevicePath = "/dev/vfio"
	CheckInterval  = 5 * time.Second
)

var (
	// Track known VFIO device groups
	knownGroups = make(map[string]bool)
)

func InitializeGroups() error {
	files, err := os.ReadDir(VfioDevicePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("VFIO directory %s not found, will monitor for creation", VfioDevicePath)
			return nil
		}
		return fmt.Errorf("error reading VFIO directory: %w", err)
	}

	for _, file := range files {
		name := file.Name()
		if isValidGroup(name) {
			knownGroups[name] = true
			log.Printf("Discovered initial IOMMU group: %s", name)
		}
	}
	return nil
}

func CheckCurrentGroups() (added, removed []string, err error) {
	files, err := os.ReadDir(VfioDevicePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Consider all groups removed if directory disappears
			return nil, getGroupKeys(), nil
		}
		return nil, nil, fmt.Errorf("error reading VFIO directory: %w", err)
	}

	currentGroups := make(map[string]bool)
	for _, file := range files {
		if name := file.Name(); isValidGroup(name) {
			currentGroups[name] = true
		}
	}
	added, removed = calculateGroupChanges(currentGroups)
	return added, removed, nil
}

func UpdateKnownGroups(added, removed []string) {
	for _, group := range added {
		knownGroups[group] = true
	}
	for _, group := range removed {
		delete(knownGroups, group)
	}
}

// Helper functions
func isValidGroup(name string) bool {
	return name != "vfio" && name != "." && name != ".."
}

func getGroupKeys() []string {
	keys := make([]string, 0, len(knownGroups))
	for k := range knownGroups {
		keys = append(keys, k)
	}
	return keys
}

func calculateGroupChanges(current map[string]bool) (added, removed []string) {
	// Detect new groups
	for group := range current {
		if !knownGroups[group] {
			added = append(added, group)
		}
	}

	// Detect removed groups
	for group := range knownGroups {
		if !current[group] {
			removed = append(removed, group)
		}
	}

	return added, removed
}
