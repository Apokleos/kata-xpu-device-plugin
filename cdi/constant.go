package cdi

import (
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
)

// Constants to represent the various device list strategies
const (
	DeviceListStrategyCDIAnnotations = "cdi-annotations"
	DeviceListStrategyCDICRI         = "cdi-cri"
	DefaultCDIAnnotationPrefix       = cdiapi.AnnotationPrefix
)
