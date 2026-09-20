package main

import (
	"testing"
)

func TestParseComposePort(t *testing.T) {
	cases := []struct {
		in            string
		containerPort int
		hostPort      int
		hostIP        string
		proto         string
	}{
		{"80", 80, 0, "", "tcp"},
		{"8080:80", 80, 8080, "", "tcp"},
		{"127.0.0.1:8080:80", 80, 8080, "127.0.0.1", "tcp"},
		{"53:53/udp", 53, 53, "", "udp"},
	}
	for _, c := range cases {
		pb, err := parseComposePort(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if pb.ContainerPort != c.containerPort || pb.Protocol != c.proto || pb.HostIP != c.hostIP {
			t.Errorf("%s → %+v", c.in, pb)
		}
		if c.hostPort == 0 && pb.HostPort != nil {
			t.Errorf("%s: hostPort = %v, want nil", c.in, *pb.HostPort)
		}
		if c.hostPort != 0 && (pb.HostPort == nil || *pb.HostPort != c.hostPort) {
			t.Errorf("%s: hostPort = %v, want %d", c.in, pb.HostPort, c.hostPort)
		}
	}
}

func TestParseComposeVolume(t *testing.T) {
	m, err := parseComposeVolume("myproj", "data:/data:ro")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != "volume" || m.Source != "myproj_data" || m.Target != "/data" || !m.ReadOnly {
		t.Errorf("named volume: %+v", m)
	}
	m, err = parseComposeVolume("myproj", "./html:/usr/share/nginx/html")
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != "bind" || m.Source != "./html" {
		t.Errorf("bind volume: %+v", m)
	}
}

func TestBuildComposePlan(t *testing.T) {
	content := `
services:
  web:
    image: nginx:1.27
    ports:
      - "8080:80"
    environment:
      FOO: bar
  db:
    image: postgres:16
    restart: always
    volumes:
      - pgdata:/var/lib/postgresql/data
`
	plan, err := buildComposePlan(DockerComposeApplyRequest{
		ProjectName: "demo",
		Content:     content,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Project != "demo" {
		t.Fatalf("project = %q", plan.Project)
	}
	if len(plan.Services) != 2 {
		t.Fatalf("services = %d", len(plan.Services))
	}
	// 文档顺序保住：web 在 db 前
	if plan.Services[0].Name != "web" || plan.Services[1].Name != "db" {
		t.Fatalf("order = %v, %v", plan.Services[0].Name, plan.Services[1].Name)
	}
	web := plan.Services[0].Request
	if web.Name != "demo-web-1" || web.Image != "nginx:1.27" {
		t.Errorf("web = %+v", web)
	}
	if web.Labels[labelProject] != "demo" || web.Labels[labelService] != "web" {
		t.Errorf("labels = %+v", web.Labels)
	}
	if web.Network != "demo_default" {
		t.Errorf("network = %q", web.Network)
	}
	db := plan.Services[1].Request
	if db.RestartPolicy != "always" {
		t.Errorf("restart = %q", db.RestartPolicy)
	}
	if len(db.Mounts) != 1 || db.Mounts[0].Source != "demo_pgdata" {
		t.Errorf("mounts = %+v", db.Mounts)
	}
	if len(plan.Networks) != 1 || plan.Networks[0] != "demo_default" {
		t.Errorf("networks = %+v", plan.Networks)
	}
}

func TestBuildComposePlanRejectsMissingImage(t *testing.T) {
	_, err := buildComposePlan(DockerComposeApplyRequest{
		ProjectName: "demo",
		Content:     "services:\n  web:\n    build: .\n",
	})
	if err == nil || err.Error() != "Service web image is required" {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildComposePlanRejectsBadProject(t *testing.T) {
	_, err := buildComposePlan(DockerComposeApplyRequest{
		ProjectName: "Bad_Name!",
		Content:     "services:\n  web:\n    image: nginx\n",
	})
	if err == nil {
		t.Fatal("want error for invalid project name")
	}
}

func TestParseFileEntries(t *testing.T) {
	output := "directory\t4096\t1700000000\t/etc\nregular file\t12\t1700000001\t/etc/hostname\nsymbolic link\t7\t1700000002\t/bin/sh\n"
	entries := parseFileEntries(output)
	if len(entries) != 3 {
		t.Fatalf("entries = %d", len(entries))
	}
	// 目录排第一
	if entries[0].Kind != "directory" || entries[0].Name != "etc" {
		t.Errorf("entries[0] = %+v", entries[0])
	}
	var found map[string]DockerFileEntry = map[string]DockerFileEntry{}
	for _, e := range entries {
		found[e.Name] = e
	}
	if found["hostname"].Kind != "file" || found["hostname"].Size != 12 {
		t.Errorf("hostname = %+v", found["hostname"])
	}
	if found["sh"].Kind != "symlink" {
		t.Errorf("sh = %+v", found["sh"])
	}
}
