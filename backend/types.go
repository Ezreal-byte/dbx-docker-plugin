package main

// 对外数据模型与 dbx docker 分支 apps/desktop/src/types/docker.ts 一一对应（camelCase JSON）。

type DockerConnectionInfo struct {
	EngineVersion     string `json:"engineVersion"`
	APIVersion        string `json:"apiVersion"`
	MinimumAPIVersion string `json:"minimumApiVersion,omitempty"`
	OperatingSystem   string `json:"operatingSystem,omitempty"`
	Architecture      string `json:"architecture,omitempty"`
}

type DockerEngineSummary struct {
	EngineVersion     string   `json:"engineVersion,omitempty"`
	APIVersion        string   `json:"apiVersion,omitempty"`
	MinimumAPIVersion string   `json:"minimumApiVersion,omitempty"`
	OperatingSystem   string   `json:"operatingSystem,omitempty"`
	Architecture      string   `json:"architecture,omitempty"`
	KernelVersion     string   `json:"kernelVersion,omitempty"`
	StorageDriver     string   `json:"storageDriver,omitempty"`
	Containers        int      `json:"containers,omitempty"`
	ContainersRunning int      `json:"containersRunning,omitempty"`
	ContainersPaused  int      `json:"containersPaused,omitempty"`
	ContainersStopped int      `json:"containersStopped,omitempty"`
	Images            int      `json:"images,omitempty"`
	DockerRootDir     string   `json:"dockerRootDir,omitempty"`
	SecurityOptions   []string `json:"securityOptions"`
	Warnings          []string `json:"warnings"`
}

type DockerEngineDetails struct {
	Version map[string]any       `json:"version"`
	Info    map[string]any       `json:"info"`
	Summary DockerEngineSummary  `json:"summary"`
}

type DockerPort struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"privatePort"`
	PublicPort  int    `json:"publicPort,omitempty"`
	PortType    string `json:"portType"`
}

type DockerContainer struct {
	ID          string            `json:"id"`
	Names       []string          `json:"names"`
	Image       string            `json:"image"`
	ImageID     string            `json:"imageId"`
	Command     string            `json:"command"`
	Created     int64             `json:"created"`
	State       string            `json:"state"`
	Status      string            `json:"status"`
	Ports       []DockerPort      `json:"ports"`
	Labels      map[string]string `json:"labels"`
	NetworkIPs  map[string]string `json:"networkIps"`
}

type DockerImage struct {
	ID          string            `json:"id"`
	RepoTags    []string          `json:"repoTags"`
	RepoDigests []string          `json:"repoDigests"`
	Created     int64             `json:"created"`
	Size        int64             `json:"size"`
	Labels      map[string]string `json:"labels"`
}

type DockerVolume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Mountpoint string            `json:"mountpoint"`
	Scope      string            `json:"scope"`
	Labels     map[string]string `json:"labels"`
}

type DockerNetwork struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	Scope      string            `json:"scope"`
	Internal   bool              `json:"internal"`
	Attachable bool              `json:"attachable"`
	Labels     map[string]string `json:"labels"`
}

type DockerContainerStats struct {
	ContainerID   string  `json:"containerId"`
	ReadAt        string  `json:"readAt"`
	CPUPercent    float64 `json:"cpuPercent"`
	MemoryUsage   uint64  `json:"memoryUsage"`
	MemoryLimit   uint64  `json:"memoryLimit"`
	MemoryPercent float64 `json:"memoryPercent"`
	NetworkRx     uint64  `json:"networkRx"`
	NetworkTx     uint64  `json:"networkTx"`
	BlockRead     uint64  `json:"blockRead"`
	BlockWrite    uint64  `json:"blockWrite"`
}

type DockerPortBinding struct {
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
	HostIP        string `json:"hostIp"`
	HostPort      *int   `json:"hostPort,omitempty"`
}

type DockerMountInput struct {
	Type     string `json:"type"` // bind | volume
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

type DockerCreateContainerRequest struct {
	Name          string              `json:"name"`
	Image         string              `json:"image"`
	Command       []string            `json:"command"`
	Environment   []string            `json:"environment"`
	Ports         []DockerPortBinding `json:"ports"`
	Mounts        []DockerMountInput  `json:"mounts"`
	Labels        map[string]string   `json:"labels"`
	Network       string              `json:"network,omitempty"`
	RestartPolicy string              `json:"restartPolicy"`
	Start         bool                `json:"start"`
}

type DockerCreateContainerResult struct {
	ID       string   `json:"id"`
	Warnings []string `json:"warnings"`
}

type DockerComposeApplyRequest struct {
	ProjectName     string `json:"projectName"`
	Content         string `json:"content"`
	ReplaceExisting bool   `json:"replaceExisting"`
}

type DockerComposeApplyResult struct {
	ContainerIDs []string `json:"containerIds"`
	Warnings     []string `json:"warnings"`
}

type DockerRegistryAuth struct {
	ServerAddress string `json:"serverAddress"`
	Username      string `json:"username"`
	Password      string `json:"password"`
}

type DockerCreateVolumeRequest struct {
	Name          string            `json:"name"`
	Driver        string            `json:"driver"`
	Labels        map[string]string `json:"labels"`
	DriverOptions map[string]string `json:"driverOptions"`
}

type DockerCreateNetworkRequest struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Internal   bool   `json:"internal"`
	Attachable bool   `json:"attachable"`
	Subnet     string `json:"subnet,omitempty"`
	Gateway    string `json:"gateway,omitempty"`
}

type DockerCreateNetworkResult struct {
	ID      string `json:"id"`
	Warning string `json:"warning"`
}

type DockerLogOptions struct {
	Tail       int  `json:"tail"`
	Timestamps bool `json:"timestamps"`
}

type DockerFileEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"` // directory | file | symlink
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type DockerFilePreview struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
}
