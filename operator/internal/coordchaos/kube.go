/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Kubectl runs kubectl against one explicit kubeconfig and context. The harness never
// relies on the current context: a chaos tool that follows whatever kubectl points at
// would one day hit the wrong cluster.
type Kubectl struct {
	Bin        string
	Kubeconfig string
	Context    string
	// Exec runs a command; nil runs the real kubectl. Tests replace it.
	Exec func(ctx context.Context, stdin []byte, args []string) ([]byte, error)
}

func (k Kubectl) args(rest []string) []string {
	out := make([]string, 0, len(rest)+4)
	if k.Kubeconfig != "" {
		out = append(out, "--kubeconfig", k.Kubeconfig)
	}
	return append(append(out, "--context", k.Context), rest...)
}

// Run runs kubectl with the arguments and returns its standard output.
func (k Kubectl) Run(ctx context.Context, stdin []byte, rest ...string) ([]byte, error) {
	args := k.args(rest)
	if k.Exec != nil {
		return k.Exec(ctx, stdin, args)
	}
	bin := k.Bin
	if bin == "" {
		bin = "kubectl"
	}
	cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- the harness builds the arguments
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("kubectl %s: %w: %s", strings.Join(rest, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// Command returns a command that streams from kubectl, for the asker.
func (k Kubectl) Command(ctx context.Context, rest ...string) *exec.Cmd {
	bin := k.Bin
	if bin == "" {
		bin = "kubectl"
	}
	return exec.CommandContext(ctx, bin, k.args(rest)...) // #nosec G204 -- the harness builds the arguments
}

// KubeCluster is the Cluster over kubectl.
type KubeCluster struct {
	K         Kubectl
	Namespace string
	// Agent is the coordinator Agent's name.
	Agent string
	// Port is the agent's HTTP port, where /q/metrics is served.
	Port int
	// Signaler sends STOP and CONT.
	Signaler *NodeSignaler
}

// podItem is the part of one pod in `kubectl get pods -o json` the harness reads.
type podItem struct {
	Metadata struct {
		Name              string  `json:"name"`
		UID               string  `json:"uid"`
		DeletionTimestamp *string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name string `json:"name"`
			Env  []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		ContainerStatuses []struct {
			Name        string `json:"name"`
			ContainerID string `json:"containerID"`
			State       struct {
				Waiting *struct {
					Reason string `json:"reason"`
				} `json:"waiting"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}

const agentContainer = "agent"

// ParsePods reads the coordinator pods out of `kubectl get pods -o json`, leaving out a
// pod that is being deleted.
func ParsePods(raw []byte) ([]Pod, error) {
	var l struct {
		Items []podItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("reading the pod list: %w", err)
	}
	pods := make([]Pod, 0, len(l.Items))
	for i := range l.Items {
		if l.Items[i].Metadata.DeletionTimestamp == nil {
			pods = append(pods, l.Items[i].pod())
		}
	}
	return pods, nil
}

func (it *podItem) pod() Pod {
	p := Pod{Name: it.Metadata.Name, UID: it.Metadata.UID, Node: it.Spec.NodeName, Env: map[string]string{}}
	for _, c := range it.Spec.Containers {
		if c.Name != agentContainer {
			continue
		}
		for _, e := range c.Env {
			p.Env[e.Name] = e.Value
		}
	}
	for _, s := range it.Status.ContainerStatuses {
		if s.Name != agentContainer {
			continue
		}
		p.ContainerID = s.ContainerID
		if s.State.Waiting != nil {
			p.WaitingReason = s.State.Waiting.Reason
		}
	}
	for _, c := range it.Status.Conditions {
		p.Ready = p.Ready || (c.Type == "Ready" && c.Status == "True")
	}
	return p
}

// CoordinatorPods lists the live pods of the coordinator Agent.
func (c *KubeCluster) CoordinatorPods(ctx context.Context) ([]Pod, error) {
	raw, err := c.K.Run(ctx, nil, "get", "pods", "-n", c.Namespace, "-l", "kubemoot.ai/agent="+c.Agent, "-o", "json")
	if err != nil {
		return nil, err
	}
	return ParsePods(raw)
}

// SetReplicas patches the coordinator Agent's replicas.
func (c *KubeCluster) SetReplicas(ctx context.Context, n int) error {
	patch := fmt.Sprintf(`{"spec":{"deployment":{"replicas":%d}}}`, n)
	_, err := c.K.Run(ctx, nil, "patch", "agent", c.Agent, "-n", c.Namespace, "--type", "merge", "-p", patch)
	return err
}

// Kill force-deletes the pod with a grace period of 0, so the container gets SIGKILL and no
// shutdown hook runs: the crash case, not a rolling update.
func (c *KubeCluster) Kill(ctx context.Context, pod Pod) error {
	_, err := c.K.Run(ctx, nil, "delete", "pod", pod.Name, "-n", c.Namespace, "--grace-period=0", "--force", "--wait=false")
	return err
}

// Signal sends STOP or CONT to the processes of the pod's agent container.
func (c *KubeCluster) Signal(ctx context.Context, pod Pod, sig string) error {
	if c.Signaler == nil {
		return fmt.Errorf("no signaler configured")
	}
	return c.Signaler.Signal(ctx, pod, sig)
}

// Counters reads /q/metrics of the pod through the API server's pod proxy.
func (c *KubeCluster) Counters(ctx context.Context, pod Pod) (Counters, error) {
	port := c.Port
	if port == 0 {
		port = 8080
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s:%d/proxy/q/metrics", c.Namespace, pod.Name, port)
	raw, err := c.K.Run(ctx, nil, "get", "--raw", path)
	if err != nil {
		return nil, err
	}
	return ParseCounters(string(raw)), nil
}

// Replicas returns the coordinator Agent's configured replicas (1 when it sets none).
func (c *KubeCluster) Replicas(ctx context.Context) (int, error) {
	raw, err := c.K.Run(ctx, nil, "get", "agent", c.Agent, "-n", c.Namespace, "-o", "jsonpath={.spec.deployment.replicas}")
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("replicas %q: %w", s, err)
	}
	return n, nil
}

// ConfiguredFlags reads the coordinator section of the KubemootConfig.
func (c *KubeCluster) ConfiguredFlags(ctx context.Context, config string) (Flags, error) {
	raw, err := c.K.Run(ctx, nil, "get", "kubemootconfig", config, "-o", "json")
	if err != nil {
		return Flags{}, err
	}
	var cfg struct {
		Spec struct {
			Coordinator Flags `json:"coordinator"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Flags{}, fmt.Errorf("reading the KubemootConfig: %w", err)
	}
	return cfg.Spec.Coordinator, nil
}

var hexID = regexp.MustCompile(`^[0-9a-f]{12,128}$`)

// ContainerHexID strips the runtime scheme from a container id such as containerd://abc.
func ContainerHexID(id string) (string, error) {
	if i := strings.Index(id, "://"); i >= 0 {
		id = id[i+3:]
	}
	if !hexID.MatchString(id) {
		return "", fmt.Errorf("container id %q is not a hex id", id)
	}
	return id, nil
}
