package cdi

import (
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
)

// QualifiedName constructs a CDI qualified device name for the specified resources.
// Note: This assumes that the specified id matches the device name returned by the naming strategy.
func QualifiedName(vendor, class, id string) string {
	return cdiparser.QualifiedName(vendor, class, id)
}
