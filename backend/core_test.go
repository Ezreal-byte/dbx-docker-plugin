package main

import (
	"encoding/json"
	"testing"

	"github.com/docker/docker/api/types/container"
)

// 锚定分支 service.rs 的单元测试：{total:300, pre:100, system:1000, pre:500, online_cpus:2} → 80%。
func TestStatsCPUFormula(t *testing.T) {
	raw := `{
		"read": "2024-01-01T00:00:00Z",
		"cpu_stats": {
			"cpu_usage": {"total_usage": 300, "percpu_usage": [150, 150]},
			"system_cpu_usage": 1000,
			"online_cpus": 2
		},
		"precpu_stats": {
			"cpu_usage": {"total_usage": 100},
			"system_cpu_usage": 500
		},
		"memory_stats": {"usage": 400, "limit": 1000, "stats": {"total_inactive_file": 100}}
	}`
	var v container.StatsResponse
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	stats := statsFromResponse("abc", &v)
	if stats.CPUPercent != 80.0 {
		t.Fatalf("cpuPercent = %v, want 80.0", stats.CPUPercent)
	}
	if stats.MemoryUsage != 300 {
		t.Fatalf("memoryUsage = %v, want 300", stats.MemoryUsage)
	}
	if stats.MemoryPercent != 30.0 {
		t.Fatalf("memoryPercent = %v, want 30.0", stats.MemoryPercent)
	}
}

func TestStatsZeroSystemDelta(t *testing.T) {
	var v container.StatsResponse
	stats := statsFromResponse("abc", &v)
	if stats.CPUPercent != 0 {
		t.Fatalf("cpuPercent = %v, want 0", stats.CPUPercent)
	}
}

func TestDecodeMultiplexedBytes(t *testing.T) {
	// 帧: [1,0,0,0, 0,0,0,5] + "hello"
	frame := []byte{1, 0, 0, 0, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'}
	out := decodeMultiplexedBytes(frame)
	if string(out) != "hello" {
		t.Fatalf("got %q, want %q", out, "hello")
	}
}

func TestDecodeMultiplexedBytesRaw(t *testing.T) {
	// 首字节 >2 → 裸文本原样返回（tty 容器）
	raw := []byte("plain tty output")
	out := decodeMultiplexedBytes(raw)
	if string(out) != string(raw) {
		t.Fatalf("got %q, want %q", out, raw)
	}
}

func TestStreamDecoderIncremental(t *testing.T) {
	d := newStreamDecoder()
	// 两个帧，分三次喂入
	data := []byte{1, 0, 0, 0, 0, 0, 0, 3, 'f', 'o', 'o', 2, 0, 0, 0, 0, 0, 0, 3, 'b', 'a', 'r'}
	out1 := d.feed(data[:5])
	if len(out1) != 0 {
		t.Fatalf("partial header should yield nothing, got %q", out1)
	}
	out2 := d.feed(data[5:13])
	if string(out2) != "foo" {
		t.Fatalf("got %q, want foo", out2)
	}
	out3 := d.feed(data[13:])
	if string(out3) != "bar" {
		t.Fatalf("got %q, want bar", out3)
	}
	if rest := d.flush(); len(rest) != 0 {
		t.Fatalf("flush = %q, want empty", rest)
	}
}

func TestEncodeRegistryAuth(t *testing.T) {
	got := encodeRegistryAuth(DockerRegistryAuth{ServerAddress: "reg.example.com", Username: "u", Password: "p"})
	// {"password":"p","serveraddress":"reg.example.com","username":"u"}
	want := "eyJwYXNzd29yZCI6InAiLCJzZXJ2ZXJhZGRyZXNzIjoicmVnLmV4YW1wbGUuY29tIiwidXNlcm5hbWUiOiJ1In0"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if encodeRegistryAuth(DockerRegistryAuth{}) != "" {
		t.Fatal("empty auth should yield empty string")
	}
}

func TestSplitPushReference(t *testing.T) {
	cases := []struct{ in, repo, tag string }{
		{"nginx", "nginx", "latest"},
		{"nginx:1.27", "nginx", "1.27"},
		{"reg.example.com:5000/team/app", "reg.example.com:5000/team/app", "latest"},
		{"reg.example.com:5000/team/app:v2", "reg.example.com:5000/team/app", "v2"},
	}
	for _, c := range cases {
		repo, tag := splitPushReference(c.in)
		if repo != c.repo || tag != c.tag {
			t.Errorf("%s → (%q,%q), want (%q,%q)", c.in, repo, tag, c.repo, c.tag)
		}
	}
}
