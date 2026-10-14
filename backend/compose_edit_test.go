package main

import "testing"

func TestComposeEditorExternalReferences(t *testing.T) {
	plan, err := buildComposePlan(DockerComposeApplyRequest{ProjectName: "demo", Content: `services:
  web:
    image: nginx
    entrypoint: [/entrypoint.sh]
    user: "1000"
    working_dir: /app
    restart: on-failure:3
    healthcheck:
      test: [CMD, echo, healthy]
      interval: 30s
      retries: 3
    ports: ["[::1]:8080:80/tcp", "127.0.0.1:8080:80/tcp"]
    networks: [demo_default]
    volumes: ["demo_data:/data:ro"]
networks:
  demo_default: {external: true, name: demo_default}
volumes:
  demo_data: {external: true, name: demo_data}
`})
	if err != nil {
		t.Fatal(err)
	}
	req := plan.Services[0].Request
	if req.Network != "demo_default" || req.Mounts[0].Source != "demo_data" || len(plan.Networks) != 0 {
		t.Fatal(plan)
	}
	if len(plan.ExternalNetworks) != 1 || len(plan.ExternalVolumes) != 1 {
		t.Fatal(plan)
	}
	config, host, _, _, err := prepareCreateContainer(req)
	if err != nil || config.User != "1000" || config.WorkingDir != "/app" || config.Entrypoint[0] != "/entrypoint.sh" || config.Healthcheck.Retries != 3 || host.RestartPolicy.MaximumRetryCount != 3 || len(host.PortBindings["80/tcp"]) != 2 {
		t.Fatal(config, host, err)
	}
}

func TestComposeHostNetworkAndWindowsMount(t *testing.T) {
	plan, err := buildComposePlan(DockerComposeApplyRequest{ProjectName: "demo", Content: "services:\n  app:\n    image: alpine\n    network_mode: host\n"})
	if err != nil {
		t.Fatal(err)
	}
	_, host, net, _, err := prepareCreateContainer(plan.Services[0].Request)
	if err != nil || string(host.NetworkMode) != "host" || net != nil {
		t.Fatal(host, net, err)
	}
	mount, err := parseComposeVolume("demo", `C:\data:/data:ro`)
	if err != nil || mount.Source != `C:\data` || mount.Type != "bind" {
		t.Fatal(mount, err)
	}
}
