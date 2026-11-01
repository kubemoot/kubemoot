/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// NodeSignaler sends STOP and CONT to a coordinator container from outside its PID
// namespace.
//
// The obvious route does not work. A coordinator image is distroless, so there is no
// shell to exec into, and an ephemeral container that targets the coordinator joins the
// coordinator's PID namespace, where the agent is process 1. Linux drops a signal sent to
// the init process of a PID namespace by a member of that same namespace unless init
// installed a handler, and SIGSTOP is never delivered that way. A signal from an
// ancestor namespace is delivered. The node's own PID namespace is the ancestor, so the
// signaler runs one privileged pod with hostPID on the coordinator's node and signals
// the container's processes from there, found by the container id in their cgroup. This
// works on any node whose runtime puts the container id in the cgroup path (containerd and
// CRI-O do, on Talos and on kind), and it needs a namespace that admits a privileged pod.
type NodeSignaler struct {
	K         Kubectl
	Namespace string
	// Image has sh, grep, cut and kill. The nats-box image the chart already pins does.
	Image string

	mu   sync.Mutex
	pods map[string]string // node -> pod name
}

const debugLabel = "app.kubernetes.io/managed-by=coordinator-chaos"

// Signal sends sig (STOP or CONT) to every process of the pod's agent container.
func (s *NodeSignaler) Signal(ctx context.Context, pod Pod, sig string) error {
	if sig != sigStop && sig != sigCont {
		return fmt.Errorf("signal %q is not STOP or CONT", sig)
	}
	id, err := ContainerHexID(pod.ContainerID)
	if err != nil {
		return err
	}
	dbg, err := s.podOn(ctx, pod.Node)
	if err != nil {
		return err
	}
	_, err = s.K.Run(ctx, nil, "exec", "-n", s.Namespace, dbg, "--", "sh", "-c", signalScript(id, sig))
	return err
}

// signalScript finds the processes whose cgroup holds the container id and signals them.
// It exits 3 when it finds none, which means the id is stale.
func signalScript(id, sig string) string {
	return fmt.Sprintf(`pids=$(grep -l %q /proc/[0-9]*/cgroup 2>/dev/null | cut -d/ -f3); `+
		`[ -n "$pids" ] || { echo "no process in container %s" >&2; exit 3; }; kill -%s $pids`, id, id, sig)
}

// podOn returns the signaling pod on the node, creating it on first use.
func (s *NodeSignaler) podOn(ctx context.Context, node string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name, ok := s.pods[node]; ok {
		return name, nil
	}
	name := "chaos-signal-" + strings.ReplaceAll(node, ".", "-")
	if len(name) > 60 {
		name = name[:60]
	}
	if err := s.claim(ctx); err != nil {
		return "", err
	}
	if _, err := s.K.Run(ctx, []byte(s.manifest(name, node)), "apply", "-f", "-"); err != nil {
		return "", fmt.Errorf("creating the signaling pod on %s (the namespace %s must admit a privileged pod): %w", node, s.Namespace, err)
	}
	if _, err := s.K.Run(ctx, nil, "wait", "-n", s.Namespace, "--for=condition=Ready", "pod/"+name, "--timeout=180s"); err != nil {
		return "", err
	}
	if s.pods == nil {
		s.pods = map[string]string{}
	}
	s.pods[node] = name
	return name, nil
}

// manifest is the namespace and pod. activeDeadlineSeconds ends the pod if the harness
// dies before Cleanup.
func (s *NodeSignaler) manifest(name, node string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %[1]s
  labels:
    pod-security.kubernetes.io/enforce: privileged
    app.kubernetes.io/managed-by: coordinator-chaos
---
apiVersion: v1
kind: Pod
metadata:
  name: %[2]s
  namespace: %[1]s
  labels:
    app.kubernetes.io/managed-by: coordinator-chaos
spec:
  nodeName: %[3]s
  hostPID: true
  restartPolicy: Never
  terminationGracePeriodSeconds: 0
  activeDeadlineSeconds: 7200
  tolerations:
    - operator: Exists
  containers:
    - name: sh
      image: %[4]s
      command: ["sleep", "7200"]
      securityContext:
        privileged: true
`, s.Namespace, name, node, s.Image)
}

// ContinueAll sends CONT to the pods, so a coordinator the run froze is not left frozen,
// and returns the failures. It does nothing when the run never created a signaling pod.
func (s *NodeSignaler) ContinueAll(ctx context.Context, pods []Pod) []error {
	s.mu.Lock()
	used := len(s.pods) > 0
	s.mu.Unlock()
	if !used {
		return nil
	}
	var errs []error
	for _, p := range pods {
		if err := s.Signal(ctx, p, sigCont); err != nil {
			errs = append(errs, fmt.Errorf("CONT to %s: %w", p.Name, err))
		}
	}
	return errs
}

const ownerLabel = "coordinator-chaos"

// namespaceOwner reports whether the signaling namespace exists and whether the chaos
// test made it (it carries the managed-by label the manifest sets).
func (s *NodeSignaler) namespaceOwner(ctx context.Context) (exists, ours bool, err error) {
	out, err := s.K.Run(ctx, nil, "get", "namespace", s.Namespace, "--ignore-not-found", "-o", "name")
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return false, false, err
	}
	label, err := s.K.Run(ctx, nil, "get", "namespace", s.Namespace, "-o",
		`jsonpath={.metadata.labels.app\.kubernetes\.io/managed-by}`)
	if err != nil {
		return true, false, err
	}
	return true, strings.TrimSpace(string(label)) == ownerLabel, nil
}

// claim refuses a namespace that exists and is not the signaler's own, so a typo in
// --signal-namespace cannot relax the pod security of kube-system or a crew namespace.
func (s *NodeSignaler) claim(ctx context.Context) error {
	exists, ours, err := s.namespaceOwner(ctx)
	if err != nil {
		return err
	}
	if exists && !ours {
		return fmt.Errorf("namespace %s exists and was not created by the chaos test; pick another --signal-namespace", s.Namespace)
	}
	return nil
}

// Cleanup deletes the signaling pods and, when the run created one, the namespace. It
// deletes the namespace only if it carries the label the test sets, and never when no
// signaling pod was made.
func (s *NodeSignaler) Cleanup(ctx context.Context) error {
	s.mu.Lock()
	used := len(s.pods) > 0
	s.mu.Unlock()
	if !used {
		return nil
	}
	if _, err := s.K.Run(ctx, nil, "delete", "pod", "-n", s.Namespace, "-l", debugLabel, "--ignore-not-found",
		"--grace-period=0", "--force"); err != nil {
		return err
	}
	exists, ours, err := s.namespaceOwner(ctx)
	if err != nil || !exists || !ours {
		return err
	}
	_, err = s.K.Run(ctx, nil, "delete", "namespace", s.Namespace, "--ignore-not-found", "--wait=false")
	return err
}
