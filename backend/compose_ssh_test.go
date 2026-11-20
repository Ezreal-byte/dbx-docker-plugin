package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestComposeSSHHostCommandsShareQuotaAndCancel(t *testing.T) {
	commands := make(chan string, 2)
	f := startLimitedSSHServerWithHook(t, 10, 0, func(command string, ch ssh.Channel, requests <-chan *ssh.Request) bool {
		go ssh.DiscardRequests(requests)
		commands <- command
		if strings.Contains(command, "wait-for-cancel") {
			for range requests {
			}
			return true
		}
		io.WriteString(ch, "source text")
		ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		return true
	})
	b := f.bridge(t)
	sess := &session{client: &dockerClient{ssh: b}}
	dir := "/srv/app's project"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := hostCommand(ctx, sess, dir, []string{"head", "-c", "128", "--", "/srv/config; echo injected"}, "", 128)
	if err != nil || string(out) != "source text" {
		t.Fatal(string(out), err)
	}
	command := <-commands
	if command != "cd -- '/srv/app'\"'\"'s project' && 'head' '-c' '128' '--' '/srv/config; echo injected'" {
		t.Fatal("unquoted host command", command)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if _, err = hostCommand(ctx2, sess, "/srv", []string{"wait-for-cancel"}, "", 128); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("SSH cancellation did not return", err)
	}
	deadline := time.Now().Add(time.Second)
	for len(b.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(b.slots) != 0 {
		t.Fatal("SSH host command leaked a slot")
	}
	for range sshNCMaxSessions {
		b.slots <- struct{}{}
	}
	ctx3, cancel3 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel3()
	if _, err = hostCommand(ctx3, sess, "/srv", []string{"head"}, "", 128); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("host command bypassed SSH quota", err)
	}
	for range sshNCMaxSessions {
		<-b.slots
	}
}
