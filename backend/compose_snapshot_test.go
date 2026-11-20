package main

import (
	"encoding/json"
	"testing"

	"github.com/docker/docker/api/types/container"
)

func TestComposeSnapshotIgnoresNetworkTeardownButBindsConfiguration(t *testing.T) {
	const original = `{"Id":"container-id","Name":"/demo","Image":"sha256:image","State":{"Status":"exited","StartedAt":"start","FinishedAt":"finish"},"Config":{"Image":"alpine","Env":["MODE=test"]},"HostConfig":{"RestartPolicy":{"Name":"no"}},"NetworkSettings":{"SandboxID":"sandbox","Networks":{"demo":{"EndpointID":"endpoint","IPAddress":"172.20.0.2","Aliases":["app"],"IPAMConfig":{"IPv4Address":"172.20.0.2"}}}}}`
	var inspected container.InspectResponse
	if err := json.Unmarshal([]byte(original), &inspected); err != nil {
		t.Fatal(err)
	}
	want := hashValue(composeContainerSnapshot(inspected))
	inspected.NetworkSettings.SandboxID = ""
	inspected.NetworkSettings.Networks["demo"].EndpointID = ""
	inspected.NetworkSettings.Networks["demo"].IPAddress = ""
	if hashValue(composeContainerSnapshot(inspected)) != want {
		t.Fatal("asynchronous network teardown invalidated a completed-stop preview")
	}
	for name, mutate := range map[string]func(*container.InspectResponse){
		"container identity": func(c *container.InspectResponse) { c.ID = "replacement" },
		"lifecycle":          func(c *container.InspectResponse) { c.State.StartedAt = "restarted" },
		"configuration":      func(c *container.InspectResponse) { c.Config.Env = []string{"MODE=production"} },
		"restart policy":     func(c *container.InspectResponse) { c.HostConfig.RestartPolicy.Name = "always" },
		"network membership": func(c *container.InspectResponse) { delete(c.NetworkSettings.Networks, "demo") },
		"network alias":      func(c *container.InspectResponse) { c.NetworkSettings.Networks["demo"].Aliases = []string{"changed"} },
		"static IP": func(c *container.InspectResponse) {
			c.NetworkSettings.Networks["demo"].IPAMConfig.IPv4Address = "172.20.0.3"
		},
	} {
		t.Run(name, func(t *testing.T) {
			var changed container.InspectResponse
			if err := json.Unmarshal([]byte(original), &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if hashValue(composeContainerSnapshot(changed)) == want {
				t.Fatal("meaningful target change was not detected")
			}
		})
	}
}
