package compose

// Logging is an intermediate representation for logging information
type Logging struct {
	Driver  string            `yaml:",omitempty"`
	Options map[string]string `yaml:",omitempty"`
}

// PortMapping is an intermediate representation for port mapping information
type PortMapping struct {
	HostIP        string `yaml:",omitempty"`
	HostPort      int    `yaml:",omitempty"`
	ContainerIP   string `yaml:",omitempty"`
	ContainerPort int    `yaml:",omitempty"`
	Protocol      string `yaml:",omitempty"`
	Name          string `yaml:",omitempty"`
}

// PortMappings is a composite type for slices of PortMapping
type PortMappings []PortMapping

func (pm PortMappings) Len() int           { return len(pm) }
func (pm PortMappings) Swap(i, j int)      { pm[i], pm[j] = pm[j], pm[i] }
func (pm PortMappings) Less(i, j int) bool { return pm[i].ContainerPort < pm[j].ContainerPort }

// IntermediateVolume is an intermediate representation for volume information
//
//	type IntermediateVolume struct {
//		Host         string
//		Container    string
//		SourceVolume string
//		ReadOnly     bool
//	}
//
// // IntermediateVolumes is a composite type for slices of IntermediateVolume
// type IntermediateVolumes []IntermediateVolume
type IntermediateVolumes interface{}

//func (iv IntermediateVolumes) Len() int      { return len(iv) }
//func (iv IntermediateVolumes) Swap(i, j int) { iv[i], iv[j] = iv[j], iv[i] }
//func (iv IntermediateVolumes) Less(i, j int) bool {
//	return strings.Compare(iv[i].Container, iv[j].Container) < 0
//}

// Fetch is an intermediate representation for fetching information
type Fetch struct {
	URI string `yaml:",omitempty"`
}

// HealthCheck is an intermediate representation for health check information
type HealthCheck struct {
	Exec string `yaml:",omitempty"`

	HTTPPath string            `yaml:",omitempty"`
	Port     int               `yaml:",omitempty"`
	Host     string            `yaml:",omitempty"`
	Scheme   string            `yaml:",omitempty"`
	Headers  map[string]string `yaml:",omitempty"`

	Interval         int `yaml:",omitempty"`
	Timeout          int `yaml:",omitempty"`
	FailureThreshold int `yaml:",omitempty"`
}

// BuildContext is an intermediary representation for build information
type BuildContext struct {
	Context    string            `yaml:",omitempty"`
	Dockerfile string            `yaml:",omitempty"`
	Args       map[string]string `yaml:",omitempty"`
}

// Container represents the intermediate format in between input and output formats
type Container struct {
	Build           *BuildContext        `yaml:",omitempty"`
	Command         interface{}          `yaml:",omitempty"` // string or []string
	CPU             int                  `yaml:",omitempty"` // out of 1024
	DNS             []string             `yaml:",omitempty"`
	Domain          []string             `yaml:",omitempty"`
	Entrypoint      string               `yaml:",omitempty"`
	EnvFile         interface{}          `yaml:"env_file,omitempty"`
	Environment     interface{}          `yaml:",omitempty"` // []string or map[string]string
	Essential       *bool                `yaml:",omitempty"`
	Expose          []int                `yaml:",omitempty"`
	Fetch           []*Fetch             `yaml:",omitempty"` // TODO make a struct
	HealthChecks    []*HealthCheck       `yaml:",omitempty"` // TODO make a struct
	Hostname        string               `yaml:",omitempty"`
	Image           string               `yaml:",omitempty"`
	Labels          interface{}          `yaml:",omitempty"` // []string or map[string]string
	Links           []string             `yaml:",omitempty"`
	Logging         *Logging             `yaml:",omitempty"`
	Memory          int                  `yaml:",omitempty"` // in bytes
	Name            string               `yaml:",omitempty"`
	Network         []string             `yaml:",omitempty"`
	NetworkMode     string               `yaml:",omitempty"`
	Pid             string               `yaml:",omitempty"`
	Ports           []string             `yaml:",omitempty"`
	PortMappings    *PortMappings        `yaml:",omitempty"`
	Privileged      bool                 `yaml:",omitempty"`
	PullImagePolicy string               `yaml:",omitempty"`
	Replicas        int                  `yaml:",omitempty"`
	StopSignal      string               `yaml:",omitempty"`
	User            string               `yaml:",omitempty"`
	Volumes         *IntermediateVolumes `yaml:",omitempty"`
	VolumesFrom     []string             `yaml:",omitempty"` // todo make a struct
	WorkDir         string               `yaml:",omitempty"`
}

// Containers is for storing and sorting slices of Container
type Containers []Container

// DockerCompose implements InputFormat and OutputFormat
type DockerCompose struct {
	Services map[string]*Container `yaml:"services"`
}
