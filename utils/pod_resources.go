package utils

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"time"

	v1alpha1 "k8s.io/kubelet/pkg/apis/podresources/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/apis/podresources"
	"k8s.io/kubernetes/pkg/kubelet/util"
)

const (
	// unixProtocol is the network protocol of unix socket.
	unixProtocol = "unix"
)

const (
	// Kubelet internal cgroup name for node allocatable cgroup.
	defaultNodeAllocatableCgroup = "kubepods"
	// defaultPodResourcesPath is the path to the local endpoint serving the podresources GRPC service.
	defaultPodResourcesPath    = "/var/lib/kubelet/pod-resources"
	defaultPodResourcesTimeout = 10 * time.Second
	defaultPodResourcesMaxSize = 1024 * 1024 * 16 // 16 Mb

)

// LocalEndpoint returns the full path to a unix socket at the given endpoint
func LocalEndpoint(path, file string) (string, error) {
	log.Printf("endpoint Path: %v, file: %v", path, file)
	u := url.URL{
		Scheme: unixProtocol,
		Path:   path,
	}
	return filepath.Join(u.String(), file+".sock"), nil
}

func GetV1alpha1PodResources(ctx context.Context) (*v1alpha1.ListPodResourcesResponse, error) {
	endpoint, err := util.LocalEndpoint(defaultPodResourcesPath, podresources.Socket)
	if err != nil {
		return nil, fmt.Errorf("Error getting local endpoint: %w", err)
	}
	client, conn, err := podresources.GetV1alpha1Client(endpoint, defaultPodResourcesTimeout, defaultPodResourcesMaxSize)
	if err != nil {
		return nil, fmt.Errorf("Error getting grpc client: %w", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := client.List(ctx, &v1alpha1.ListPodResourcesRequest{})
	if err != nil {
		return nil, fmt.Errorf("%v.Get(_) = _, %v", client, err)
	}

	log.Printf("Pod Resources Response %#v", resp)

	return resp, nil
}
