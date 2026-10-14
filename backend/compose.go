package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"gopkg.in/yaml.v3"
)

// compose 子集：image/container_name/command/environment/短语法 ports/短语法 volumes/
// networks/restart/labels。不支持 build、depends_on 健康条件、.env、扩展字段。
// 行为逐字复刻分支 compose.rs（含标签、命名、回滚与错误后缀文案）。

const (
	labelProject         = "com.docker.compose.project"
	labelService         = "com.docker.compose.service"
	labelContainerNumber = "com.docker.compose.container-number"
	labelOneoff          = "com.docker.compose.oneoff"
)

var projectNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type composePlan struct {
	Project          string
	Services         []composeService
	Networks         []string // 去重排序后的 {project}_{name} 形式
	ExternalNetworks []string
	ExternalVolumes  []string
}

type composeService struct {
	Name    string
	Request DockerCreateContainerRequest
}

func (p *plugin) applyCompose(sess *session, raw json.RawMessage) (any, error) {
	var in struct {
		Request DockerComposeApplyRequest `json:"request"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errInvalidParams
	}
	if err := p.ensureWritable(sess, "applying Compose projects"); err != nil {
		return nil, err
	}
	sess.writeMutex().Lock()
	defer sess.writeMutex().Unlock()

	plan, err := buildComposePlan(in.Request)
	if err != nil {
		return nil, err
	}
	return p.applyComposePlan(sess, plan, in.Request.ReplaceExisting)
}

func validateProjectName(name string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if len(trimmed) == 0 || len(trimmed) > 63 || !projectNamePattern.MatchString(trimmed) {
		return "", fmt.Errorf("Invalid Compose project name: %q", name)
	}
	return trimmed, nil
}

func buildComposePlan(req DockerComposeApplyRequest) (*composePlan, error) {
	project, err := validateProjectName(req.ProjectName)
	if err != nil {
		return nil, err
	}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(req.Content), &doc); err != nil {
		return nil, fmt.Errorf("Failed to parse Compose YAML: %v", err)
	}
	if len(req.Content) > 128*1024 {
		return nil, errors.New("Compose content exceeds 128 KiB")
	}
	if strings.Contains(req.Content, "${") {
		return nil, errors.New("Compose variable interpolation is not supported in this version")
	}
	external := map[string]map[string]string{"networks": {}, "volumes": {}}
	for key, value := range doc {
		switch key {
		case "services", "version":
		case "networks", "volumes":
			if value == nil {
				continue
			}
			declared, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Compose %s must be a mapping", key)
			}
			for name, config := range declared {
				if config == nil {
					continue
				}
				m, ok := config.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("Compose %s.%s must be a mapping", key, name)
				}
				if len(m) == 0 {
					continue
				}
				if m["external"] != true {
					return nil, fmt.Errorf("Compose %s.%s: only empty or external resource declarations are supported", key, name)
				}
				for field := range m {
					if field != "external" && field != "name" {
						return nil, fmt.Errorf("Unsupported external resource field: %s.%s.%s", key, name, field)
					}
				}
				actual := name
				if n, exists := m["name"]; exists {
					var valid bool
					actual, valid = n.(string)
					if !valid {
						return nil, fmt.Errorf("External resource name must be a string")
					}
				}
				if _, err := validateResourceName(actual, "external resource"); err != nil {
					return nil, err
				}
				external[key][project+"_"+name] = actual
			}
		default:
			return nil, fmt.Errorf("Unsupported Compose field: %s", key)
		}
	}
	servicesRaw, ok := doc["services"].(map[string]any)
	if !ok || len(servicesRaw) == 0 {
		return nil, errors.New("Compose document requires a non-empty services map")
	}

	plan := &composePlan{Project: project}
	usedNames := map[string]bool{}
	networkSet := map[string]bool{}

	// 文档顺序处理 service（yaml.v3 解到 map 会丢序，改为保序解析）
	servicesOrdered, err := parseServicesInOrder([]byte(req.Content))
	if err != nil {
		return nil, err
	}
	_ = servicesRaw

	for _, svc := range servicesOrdered {
		planned, networkName, err := planComposeService(project, svc.name, svc.body)
		if err != nil {
			return nil, err
		}
		if actual, ok := external["networks"][networkName]; ok {
			planned.Request.Network = actual
			plan.ExternalNetworks = append(plan.ExternalNetworks, actual)
			networkName = ""
		}
		for i, mount := range planned.Request.Mounts {
			if actual, ok := external["volumes"][mount.Source]; ok && mount.Type == "volume" {
				planned.Request.Mounts[i].Source = actual
				plan.ExternalVolumes = append(plan.ExternalVolumes, actual)
			}
		}
		if usedNames[planned.Request.Name] {
			return nil, fmt.Errorf("Compose container name %s is declared more than once", planned.Request.Name)
		}
		usedNames[planned.Request.Name] = true
		plan.Services = append(plan.Services, *planned)
		if networkName != "" {
			networkSet[networkName] = true
		}
	}

	for n := range networkSet {
		plan.Networks = append(plan.Networks, n)
	}
	sort.Strings(plan.Networks)
	return plan, nil
}

type orderedService struct {
	name string
	body map[string]any
}

// parseServicesInOrder 用 yaml.Node 保住 services 的文档顺序。
func parseServicesInOrder(content []byte) ([]orderedService, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(content, &node); err != nil {
		return nil, fmt.Errorf("Failed to parse Compose YAML: %v", err)
	}
	if len(node.Content) == 0 {
		return nil, errors.New("Compose document is empty")
	}
	root := node.Content[0]
	var servicesNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "services" {
			servicesNode = root.Content[i+1]
			break
		}
	}
	if servicesNode == nil || servicesNode.Kind != yaml.MappingNode || len(servicesNode.Content) == 0 {
		return nil, errors.New("Compose document requires a non-empty services map")
	}
	var out []orderedService
	for i := 0; i+1 < len(servicesNode.Content); i += 2 {
		name := servicesNode.Content[i].Value
		var body map[string]any
		if err := servicesNode.Content[i+1].Decode(&body); err != nil {
			return nil, fmt.Errorf("Service %s must be a mapping", name)
		}
		out = append(out, orderedService{name: name, body: body})
	}
	return out, nil
}

func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case int:
		return strconv.Itoa(t), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(t), true
	default:
		return "", false
	}
}

func parseComposeHealthcheck(value any) (*container.HealthConfig, error) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("healthcheck must be a mapping")
	}
	h := &container.HealthConfig{}
	for key, value := range m {
		switch key {
		case "disable":
			if value != true && value != false {
				return nil, errors.New("healthcheck.disable must be boolean")
			}
			if value == true {
				h.Test = []string{"NONE"}
			}
		case "test":
			switch v := value.(type) {
			case string:
				h.Test = []string{"CMD-SHELL", v}
			case []any:
				for _, item := range v {
					s, ok := item.(string)
					if !ok {
						return nil, errors.New("healthcheck.test entries must be strings")
					}
					h.Test = append(h.Test, s)
				}
			default:
				return nil, errors.New("Invalid healthcheck.test")
			}
		case "interval", "timeout", "start_period", "start_interval":
			s, ok := value.(string)
			if !ok {
				return nil, errors.New("healthcheck durations must be strings")
			}
			d, err := time.ParseDuration(s)
			if err != nil || d < 0 {
				return nil, errors.New("Invalid healthcheck duration")
			}
			switch key {
			case "interval":
				h.Interval = d
			case "timeout":
				h.Timeout = d
			case "start_period":
				h.StartPeriod = d
			case "start_interval":
				h.StartInterval = d
			}
		case "retries":
			n, ok := value.(int)
			if !ok || n < 0 {
				return nil, errors.New("Invalid healthcheck retries")
			}
			h.Retries = n
		default:
			return nil, fmt.Errorf("Unsupported healthcheck field: %s", key)
		}
	}
	if m["disable"] == true {
		h.Test = []string{"NONE"}
	}
	if len(h.Test) > 0 && h.Test[0] != "NONE" && h.Test[0] != "CMD" && h.Test[0] != "CMD-SHELL" {
		return nil, errors.New("Invalid healthcheck test type")
	}
	return h, nil
}

func planComposeService(project, serviceName string, body map[string]any) (*composeService, string, error) {
	if body == nil {
		return nil, "", fmt.Errorf("Service %s must be a mapping", serviceName)
	}
	image, _ := scalarString(body["image"])
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, "", fmt.Errorf("Service %s image is required", serviceName)
	}

	for key := range body {
		switch key {
		case "image", "container_name", "command", "environment", "ports", "volumes", "networks", "restart", "labels", "entrypoint", "user", "working_dir", "network_mode", "healthcheck":
		default:
			return nil, "", fmt.Errorf("Service %s has unsupported field: %s", serviceName, key)
		}
	}
	if a, ok := body["networks"].([]any); ok && len(a) > 1 {
		return nil, "", fmt.Errorf("Service %s: multiple networks require a later Compose version", serviceName)
	}
	if m, ok := body["networks"].(map[string]any); ok {
		if len(m) > 1 {
			return nil, "", fmt.Errorf("Service %s: multiple networks are not supported", serviceName)
		}
		for _, v := range m {
			if v != nil {
				options, ok := v.(map[string]any)
				if !ok || len(options) > 0 {
					return nil, "", fmt.Errorf("Service %s: network aliases and static addresses are not supported", serviceName)
				}
			}
		}
	}

	containerName, _ := scalarString(body["container_name"])
	containerName = strings.TrimSpace(containerName)
	if containerName == "" {
		containerName = fmt.Sprintf("%s-%s-1", project, serviceName)
	}

	// command：字符串按空白切分（无 shell 引号处理），数组原样。
	var command []string
	switch cmd := body["command"].(type) {
	case string:
		command = strings.Fields(cmd)
	case []any:
		for _, item := range cmd {
			if s, ok := scalarString(item); ok {
				command = append(command, s)
			}
		}
	}

	// environment：数组原样；map 拼 K=V（null → 空串）。
	var environment []string
	switch env := body["environment"].(type) {
	case []any:
		for _, item := range env {
			if s, ok := scalarString(item); ok {
				environment = append(environment, s)
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := env[k]
			if v == nil {
				environment = append(environment, k+"=")
			} else if s, ok := scalarString(v); ok {
				environment = append(environment, k+"="+s)
			}
		}
	}

	// ports：仅短语法 [hostIp:]hostPort:]containerPort[/proto]，从右解析。
	var ports []DockerPortBinding
	switch portsRaw := body["ports"].(type) {
	case []any:
		for _, item := range portsRaw {
			s, ok := scalarString(item)
			if !ok {
				return nil, "", fmt.Errorf("Service %s ports entries must be strings", serviceName)
			}
			pb, err := parseComposePort(s)
			if err != nil {
				return nil, "", fmt.Errorf("Service %s: %v", serviceName, err)
			}
			ports = append(ports, pb)
		}
	}

	// volumes：仅短语法 source:target[:ro|rw]；named volume 改名 {project}_{source}。
	var mounts []DockerMountInput
	switch volsRaw := body["volumes"].(type) {
	case []any:
		for _, item := range volsRaw {
			s, ok := scalarString(item)
			if !ok {
				return nil, "", fmt.Errorf("Service %s volumes entries must be strings", serviceName)
			}
			m, err := parseComposeVolume(project, s)
			if err != nil {
				return nil, "", fmt.Errorf("Service %s: %v", serviceName, err)
			}
			mounts = append(mounts, m)
		}
	}

	// networks：字符串/数组取第一个/对象取第一个键；host/none 特例不挂网络。
	networkName := "default"
	switch netRaw := body["networks"].(type) {
	case string:
		networkName = netRaw
	case []any:
		if len(netRaw) > 0 {
			if s, ok := scalarString(netRaw[0]); ok {
				networkName = s
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(netRaw))
		for k := range netRaw {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			networkName = keys[0]
		}
	}

	attachNetwork := ""
	resolvedNetwork := ""
	if networkName != "host" && networkName != "none" {
		resolvedNetwork = project + "_" + networkName
		attachNetwork = resolvedNetwork
	}
	if mode, exists := body["network_mode"]; exists {
		if _, hasNetworks := body["networks"]; hasNetworks || (mode != "host" && mode != "none") {
			return nil, "", errors.New("Only host/none network_mode without networks is supported")
		}
		attachNetwork, resolvedNetwork = mode.(string), ""
	}

	// restart：剥离 :count 后缀。
	restart := "no"
	if r, ok := scalarString(body["restart"]); ok {
		restart = strings.SplitN(strings.TrimSpace(r), ":", 2)[0]
	}

	// labels：map 或 key=value 数组。
	labels := map[string]string{}
	switch labelsRaw := body["labels"].(type) {
	case map[string]any:
		for k, v := range labelsRaw {
			if s, ok := scalarString(v); ok {
				labels[k] = s
			}
		}
	case []any:
		for _, item := range labelsRaw {
			if s, ok := scalarString(item); ok {
				if idx := strings.Index(s, "="); idx >= 0 {
					labels[s[:idx]] = s[idx+1:]
				} else {
					labels[s] = ""
				}
			}
		}
	}
	labels[labelProject] = project
	labels[labelService] = serviceName
	labels[labelContainerNumber] = "1"
	labels[labelOneoff] = "False"

	req := DockerCreateContainerRequest{
		Name:          containerName,
		Image:         image,
		Command:       command,
		Environment:   environment,
		Ports:         ports,
		Mounts:        mounts,
		Labels:        labels,
		Network:       attachNetwork,
		RestartPolicy: restart,
		Start:         false,
	}
	if v, exists := body["entrypoint"]; exists {
		entries, ok := v.([]any)
		if !ok {
			return nil, "", errors.New("entrypoint must be an array")
		}
		for _, value := range entries {
			s, ok := value.(string)
			if !ok {
				return nil, "", errors.New("entrypoint entries must be strings")
			}
			req.Entrypoint = append(req.Entrypoint, s)
		}
	}
	req.User, _ = scalarString(body["user"])
	req.WorkingDir, _ = scalarString(body["working_dir"])
	if value, exists := body["healthcheck"]; exists {
		health, err := parseComposeHealthcheck(value)
		if err != nil {
			return nil, "", err
		}
		req.Healthcheck = health
	}
	if policy, ok := scalarString(body["restart"]); ok && strings.Contains(policy, ":") {
		count, err := strconv.Atoi(strings.SplitN(policy, ":", 2)[1])
		if err != nil || count < 0 || restart != "on-failure" {
			return nil, "", errors.New("Invalid restart retry count")
		}
		req.RestartRetries = count
	}
	return &composeService{Name: serviceName, Request: req}, resolvedNetwork, nil
}

// parseComposePort 解析 [hostIp:]hostPort:]containerPort[/proto]，从右往左。
func parseComposePort(raw string) (DockerPortBinding, error) {
	s := strings.TrimSpace(raw)
	proto := "tcp"
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		proto = strings.ToLower(s[idx+1:])
		s = s[:idx]
	}
	if proto != "tcp" && proto != "udp" {
		return DockerPortBinding{}, fmt.Errorf("Unsupported port protocol in %q", raw)
	}
	hostIP := ""
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]:")
		if end < 0 {
			return DockerPortBinding{}, fmt.Errorf("Invalid IPv6 port mapping: %q", raw)
		}
		hostIP, s = s[1:end], s[end+2:]
	}
	parts := strings.Split(s, ":")
	if len(parts) == 0 || len(parts) > 3 {
		return DockerPortBinding{}, fmt.Errorf("Invalid port mapping: %q", raw)
	}
	containerPort, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || containerPort <= 0 || containerPort > 65535 {
		return DockerPortBinding{}, fmt.Errorf("Invalid container port in %q", raw)
	}
	out := DockerPortBinding{ContainerPort: containerPort, Protocol: proto, HostIP: hostIP}
	if len(parts) >= 2 {
		value := parts[len(parts)-2]
		if value != "" {
			hp, err := strconv.Atoi(value)
			if err != nil || hp < 0 || hp > 65535 {
				return DockerPortBinding{}, fmt.Errorf("Invalid host port in %q", raw)
			}
			if hp > 0 {
				out.HostPort = &hp
			}
		}
	}
	if len(parts) == 3 {
		out.HostIP = parts[0]
	}
	return out, nil
}

// parseComposeVolume 解析 source:target[:ro|rw]。
func parseComposeVolume(project, raw string) (DockerMountInput, error) {
	parts := strings.Split(raw, ":")
	if len(parts) >= 3 && len(parts[0]) == 1 && (strings.HasPrefix(parts[1], "\\") || strings.HasPrefix(parts[1], "/")) {
		parts = append([]string{parts[0] + ":" + parts[1]}, parts[2:]...)
	}
	if len(parts) < 2 || len(parts) > 3 {
		return DockerMountInput{}, fmt.Errorf("Invalid volume mapping: %q", raw)
	}
	source := parts[0]
	target := parts[1]
	readOnly := false
	if len(parts) == 3 {
		mode := strings.ToLower(parts[2])
		if mode == "ro" {
			readOnly = true
		} else if mode != "rw" {
			return DockerMountInput{}, fmt.Errorf("Invalid volume mode in %q", raw)
		}
	}
	mountType := "volume"
	// 以 / 或 . 开头，或第二字符是 :（Windows 盘符）→ bind
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || (len(source) >= 2 && source[1] == ':') {
		mountType = "bind"
	} else {
		source = project + "_" + source
	}
	return DockerMountInput{Type: mountType, Source: source, Target: target, ReadOnly: readOnly}, nil
}

// ---------- 事务式 apply ----------

type containerBackup struct {
	ID            string
	OriginalName  string
	BackupName    string
	OriginalState string
}

func (p *plugin) applyComposePlan(sess *session, plan *composePlan, replaceExisting bool) (any, error) {
	cli := sess.client.cli
	ctx := sess.context()
	// Resolve external dependencies before stopping or renaming any container.
	for _, name := range plan.ExternalNetworks {
		if _, err := cli.NetworkInspect(ctx, name, network.InspectOptions{}); err != nil {
			return nil, fmt.Errorf("External network %s is unavailable: %w", name, err)
		}
	}
	for _, name := range plan.ExternalVolumes {
		if _, err := cli.VolumeInspect(ctx, name); err != nil {
			return nil, fmt.Errorf("External volume %s is unavailable: %w", name, err)
		}
	}

	allContainers, err := cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	var existingProject []container.Summary
	for _, c := range allContainers {
		if c.Labels[labelProject] == plan.Project {
			existingProject = append(existingProject, c)
			continue
		}
		// 不属于本项目但名字与计划容器名冲突 → 直接报错。
		for _, svc := range plan.Services {
			for _, n := range c.Names {
				if strings.TrimPrefix(n, "/") == svc.Request.Name {
					return nil, fmt.Errorf("Compose container name conflicts with an existing container outside project %s", plan.Project)
				}
			}
		}
	}

	if len(existingProject) > 0 && !replaceExisting {
		return nil, fmt.Errorf("Compose project %s already exists; enable replacement to update it", plan.Project)
	}
	if sess.mutationScope != nil {
		var approved struct{ Containers []struct{ ID string } }
		_ = json.Unmarshal(jsonBytes(sess.mutationScope), &approved)
		expected := []string{}
		actual := []string{}
		for _, c := range approved.Containers {
			expected = append(expected, c.ID)
		}
		for _, c := range existingProject {
			actual = append(actual, c.ID)
		}
		sort.Strings(expected)
		sort.Strings(actual)
		if hashValue(expected) != hashValue(actual) {
			return nil, errors.New("Compose project changed after preview; preview again")
		}
	}

	var backups []containerBackup
	var createdNetworks []string
	var createdContainers []string
	rollbackFrom := 0 // 0=未动旧部署 1=已动旧部署
	var warnings []string

	rollback := func(cause error) error {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		var rollbackErrs []string
		// 倒序 stop+remove 新容器
		for i := len(createdContainers) - 1; i >= 0; i-- {
			id := createdContainers[i]
			_ = cli.ContainerStop(ctx, id, container.StopOptions{})
			if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
				rollbackErrs = append(rollbackErrs, err.Error())
			}
		}
		// 倒序删网络
		for i := len(createdNetworks) - 1; i >= 0; i-- {
			if err := cli.NetworkRemove(ctx, createdNetworks[i]); err != nil {
				rollbackErrs = append(rollbackErrs, err.Error())
			}
		}
		// 倒序恢复备份
		for i := len(backups) - 1; i >= 0; i-- {
			b := backups[i]
			if err := cli.ContainerRename(ctx, b.ID, b.OriginalName); err != nil {
				rollbackErrs = append(rollbackErrs, err.Error())
				continue
			}
			if b.OriginalState == "running" || b.OriginalState == "paused" {
				if err := cli.ContainerStart(ctx, b.ID, container.StartOptions{}); err != nil {
					rollbackErrs = append(rollbackErrs, err.Error())
					continue
				}
				if b.OriginalState == "paused" {
					if err := cli.ContainerPause(ctx, b.ID); err != nil {
						rollbackErrs = append(rollbackErrs, err.Error())
					}
				}
			}
		}
		if len(rollbackErrs) > 0 {
			return fmt.Errorf("%v; rollback encountered: %s", cause, strings.Join(rollbackErrs, "; "))
		}
		if rollbackFrom == 1 {
			return fmt.Errorf("%v; previous Compose deployment was restored", cause)
		}
		if len(existingProject) > 0 {
			return fmt.Errorf("%v; previous Compose deployment was left unchanged", cause)
		}
		return fmt.Errorf("%v; replacement resources were cleaned up", cause)
	}

	// 备份计划
	for _, c := range existingProject {
		originalName := ""
		if len(c.Names) > 0 {
			originalName = strings.TrimPrefix(c.Names[0], "/")
		}
		shortID := strings.TrimPrefix(c.ID, "sha256:")
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		backups = append(backups, containerBackup{
			ID:            c.ID,
			OriginalName:  originalName,
			BackupName:    fmt.Sprintf("dbx-backup-%s-%s", plan.Project, shortID),
			OriginalState: string(c.State),
		})
	}

	// 网络：已存在则跳过。
	existingNetworks, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil, rollback(err)
	}
	networkExists := map[string]bool{}
	for _, n := range existingNetworks {
		networkExists[n.Name] = true
	}
	for _, name := range plan.Networks {
		if networkExists[name] {
			continue
		}
		if _, err := cli.NetworkCreate(ctx, name, network.CreateOptions{
			Driver:     "bridge",
			Internal:   false,
			Attachable: false,
		}); err != nil {
			return nil, rollback(err)
		}
		createdNetworks = append(createdNetworks, name)
	}

	// 替换：rename 旧容器 → 停止。
	if len(backups) > 0 {
		rollbackFrom = 1
		for _, b := range backups {
			if err := cli.ContainerRename(ctx, b.ID, b.BackupName); err != nil {
				return nil, rollback(err)
			}
		}
		for _, b := range backups {
			if b.OriginalState == "paused" {
				_ = cli.ContainerUnpause(ctx, b.ID)
			}
			if b.OriginalState == "running" || b.OriginalState == "paused" {
				if err := cli.ContainerStop(ctx, b.ID, container.StopOptions{}); err != nil {
					return nil, rollback(err)
				}
			}
		}
	}

	// 按文档顺序创建。
	for _, svc := range plan.Services {
		config, hostConfig, networkingConfig, name, err := prepareCreateContainer(svc.Request)
		if err != nil {
			return nil, rollback(err)
		}
		resp, err := cli.ContainerCreate(ctx, config, hostConfig, networkingConfig, nil, name)
		if err != nil {
			return nil, rollback(err)
		}
		if resp.ID == "" {
			return nil, rollback(errors.New("Docker created the container but did not return its ID"))
		}
		warnings = append(warnings, resp.Warnings...)
		createdContainers = append(createdContainers, resp.ID)
	}

	// 启动。
	for _, id := range createdContainers {
		if err := cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
			return nil, rollback(err)
		}
	}

	if len(plan.Services) > 1 {
		warnings = append(warnings, "Services are created in document order; depends_on health conditions are not evaluated.")
	}

	// 删除备份容器；删不掉只记 warning。
	for _, b := range backups {
		if err := cli.ContainerRemove(ctx, b.ID, container.RemoveOptions{Force: true}); err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to remove replaced container %s: %v", b.BackupName, err))
		}
	}

	return DockerComposeApplyResult{ContainerIDs: createdContainers, Warnings: warnings}, nil
}
