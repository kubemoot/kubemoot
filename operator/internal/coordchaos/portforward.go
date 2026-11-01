/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// PortForward is a kubectl port-forward the harness started and owns. It stops the
// process through its own handle; it never looks a process up by name.
type PortForward struct {
	// URL is the nats:// address on the local side.
	URL string
	// Local is the local port the forward listens on.
	Local  int
	cancel context.CancelFunc
	done   chan struct{}
}

// StartPortForward forwards a free local port to the service's port and returns once
// kubectl reports the forward is up. ready bounds that wait.
func StartPortForward(ctx context.Context, k Kubectl, namespace, service string, port int, ready time.Duration) (*PortForward, error) {
	return startForward(ctx, k, namespace, "svc/"+service, port, ready)
}

// startForward forwards a free local port to target ("svc/<name>" or "pod/<name>").
// The forward goes through the kubelet, so NetworkPolicies do not block it, unlike the
// API server's proxy, which reaches the pod over the pod network.
func startForward(ctx context.Context, k Kubectl, namespace, target string, port int, ready time.Duration) (*PortForward, error) {
	local, err := freePort()
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithCancel(ctx)
	cmd := k.Command(pctx, "port-forward", "-n", namespace, target, fmt.Sprintf("%d:%d", local, port))
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	pf := &PortForward{URL: fmt.Sprintf("nats://127.0.0.1:%d", local), Local: local, cancel: cancel, done: make(chan struct{})}
	up := make(chan struct{})
	go func() {
		defer close(pf.done)
		sc := bufio.NewScanner(out)
		signalled := false
		for sc.Scan() {
			if !signalled && strings.HasPrefix(sc.Text(), "Forwarding from") {
				signalled = true
				close(up)
			}
		}
		_ = cmd.Wait()
	}()
	select {
	case <-up:
		return pf, nil
	case <-pf.done:
		cancel()
		return nil, fmt.Errorf("port-forward to %s/%s ended before it was ready", namespace, target)
	case <-time.After(ready):
		pf.Close()
		return nil, fmt.Errorf("port-forward to %s/%s not ready after %s", namespace, target, ready)
	}
}

// Close stops the forward.
func (p *PortForward) Close() {
	p.cancel()
	<-p.done
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}
