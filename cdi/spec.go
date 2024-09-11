package cdi

import (
	"encoding/json"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// CurrentVersion is the current version of the Spec.
const CurrentVersion = "0.6.0"
const DefaultKind = "nvidia.com/gpu"
const CdiK8SPrefix = "cdi.k8s.io/"

// Spec is the base configuration for CDI
type CdiSpec struct {
	Version string `json:"cdiVersion" yaml:"cdiVersion"`
	Kind    string `json:"kind" yaml:"kind"`
	// Annotations add meta information per CDI spec. Note these are CDI-specific and do not affect container metadata.
	Annotations    map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
	Devices        []Device          `json:"devices" yaml:"devices"`
	ContainerEdits ContainerEdits    `json:"containerEdits,omitempty" yaml:"containerEdits,omitempty"`
}

type Device struct {
	Name string `json:"name" yaml:"name"`
	// Annotations add meta information per CDI spec. Note these are CDI-specific and do not affect container metadata.
	Annotations    map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
	ContainerEdits ContainerEdits    `json:"containerEdits" yaml:"containerEdits"`
}

type ContainerEdits struct {
	DeviceNodes []*DeviceNode `json:"deviceNodes,omitempty" yaml:"deviceNodes,omitempty"`
}

type DeviceNode struct {
	Path string `json:"path" yaml:"path"`
	// HostPath string `json:"hostPath,omitempty" yaml:"hostPath,omitempty"`
}

func (cs *CdiSpec) initSpec() {
	if len(cs.Devices) == 0 {
		cs.Devices = make([]Device, 0)
	}
	if len(cs.Annotations) == 0 {
		cs.Annotations = make(map[string]string)
	}
}

func New() *CdiSpec {
	cs := CdiSpec{
		Version: CurrentVersion,
		Kind:    DefaultKind,
	}
	return &cs
}

func (cs *CdiSpec) NewContainerEdits(devNode *DeviceNode) {
	devNodes := []*DeviceNode{}

	if devNode != nil {
		devNodes = append(devNodes, devNode)
	}

	ce := ContainerEdits{
		DeviceNodes: devNodes,
	}

	cs.ContainerEdits = ce
}

func (cs *CdiSpec) NewDevice(devName string, annotations map[string]string, devices []*DeviceNode) {
	cs.initSpec()

	device := Device{
		Name:           devName,
		Annotations:    annotations,
		ContainerEdits: ContainerEdits{DeviceNodes: devices},
	}

	cs.Devices = append(cs.Devices, device)
}

func (spec *CdiSpec) Save(cdiPath, fName, format string) {
	suffix := ".json"
	if format == "YAML" {
		suffix = ".yaml"
	}

	// cdiPath: "/var/run/cdi/" "/etc/cdi/"
	file_path := cdiPath + fName + suffix
	file, err := os.Create(file_path)
	if err != nil {
		fmt.Println("Error creating file:", err)
		return
	}
	defer file.Close()

	var data []byte

	switch format {
	// Encode the CdiSpec instance to YAML file
	case "YAML":
		// Create a new YAML encoder with pretty format
		encoder := yaml.NewEncoder(file)
		defer encoder.Close()
		encoder.SetIndent(2)
		if err := encoder.Encode(&spec); err != nil {
			fmt.Println("Error encoding YAML:", err)
			return
		}
	// Serialize the CdiSpec instance to JSON file
	default:
		data, err = json.MarshalIndent(spec, "", "  ")
		if err != nil {
			fmt.Println("Error marshalling JSON:", err)
			return
		}
		if _, err = file.Write(data); err != nil {
			fmt.Println("Error writing to file:", err)
			return
		}
	}

	fmt.Println("Data successfully written to file")
}

// func exampleCdiSpec() CdiSpec {
// 	return CdiSpec{
// 		Version: "0.7.0",
// 		Kind:    "nvidia.com/gpu",
// 		Annotations: map[string]string{
// 			"example.com/annotation":  "value",
// 			"example.com/annotation2": "value2",
// 		},
// 		Devices: []Device{
// 			{
// 				Name: "0",
// 				Annotations: map[string]string{
// 					"cdi.k8s.io/vfio214": "nvidia.com/gpu=0",
// 				},
// 				ContainerEdits: ContainerEdits{
// 					DeviceNodes: []*DeviceNode{
// 						{
// 							Path: "/dev/vfio/214",
// 						},
// 					},
// 				},
// 			},
// 			{
// 				Name: "1",
// 				Annotations: map[string]string{
// 					"cdi.k8s.io/vfio215": "nvidia.com/gpu=1",
// 					"bdf":                "3d:00.1",
// 					"attach-pci":         "true",
// 				},
// 				ContainerEdits: ContainerEdits{
// 					DeviceNodes: []*DeviceNode{
// 						{
// 							Path: "/dev/vfio/215",
// 						},
// 					},
// 				},
// 			},
// 			{
// 				Name: "2",
// 				Annotations: map[string]string{
// 					"cdi.k8s.io/vfio75": "nvidia.com/gpu=2",
// 					"bdf":               "c1:00.1",
// 					"attach-pci":        "true",
// 				},
// 				ContainerEdits: ContainerEdits{
// 					DeviceNodes: []*DeviceNode{
// 						{
// 							Path: "/dev/vfio/75",
// 						},
// 					},
// 				},
// 			},
// 			{
// 				Name: "3",
// 				Annotations: map[string]string{
// 					"cdi.k8s.io/vfio75": "nvidia.com/gpu=3",
// 					"bdf":               "c5:00.1",
// 					"attach-pci":        "true",
// 				},
// 				ContainerEdits: ContainerEdits{
// 					DeviceNodes: []*DeviceNode{
// 						{
// 							Path: "/dev/vfio/76",
// 						},
// 					},
// 				},
// 			},
// 		},
// 		ContainerEdits: ContainerEdits{
// 			DeviceNodes: []*DeviceNode{
// 				{
// 					Path: "/dev/vfio/78",
// 				},
// 			},
// 		},
// 	}
// }

// func Load(json_data string) {
// 	var CdiSpec CdiSpec
// 	err := json.Unmarshal([]byte(json_data), &CdiSpec)
// 	if err != nil {
// 		log.Fatalf("error: %v", err)
// 	}

// 	fmt.Printf("Parsed JSON: %+v\n", CdiSpec)
// }

// func Load2() {
//   var SpecData CdiSpec
// 	err := yaml.Unmarshal([]byte(data), &CdiSpec)
// 	if err != nil {
// 		log.Fatalf("error: %v", err)
// 	}

// 	fmt.Printf("Parsed YAML: %+v\n", CdiSpec)
// }

// func main() {
// 	cs := exampleCdiSpec()
// 	Save(cs, "/var/run/cdi", "cdi-config", "JSON")
// 	Save(cs, "/var/run/cdi", "cdi-config", "YAML")
// }
