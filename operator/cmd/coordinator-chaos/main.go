/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command coordinator-chaos is the coordinator chaos test: it asks a crew a real
// question, kills or freezes the crew's coordinator at a chosen point of the discussion,
// and checks from the coordinator log and the discussion stream that the question was
// still answered once. It is a test tool; see docs/operating/coordinator-chaos-test.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kubemoot/kubemoot/operator/internal/coordchaos"
)

type options struct {
	kubeconfig, kctx, namespace, crew, agent, natsURL string
	natsNamespace, natsService, configName            string
	debugNamespace, debugImage, outDir, cases         string
	mode, question                                    string
	port                                              int
	list, preflightOnly                               bool
	pointTimeout, settleTimeout, quiet                time.Duration
}

// defaultQuestion needs a starter crew's specialists and its reviewer, so the discussion
// passes evaluation, review and synthesis. A question the coordinator answers directly
// (for example "what is the capital of France") skips them, and those kill points are
// never reached.
const defaultQuestion = "List every pod in this namespace with its restart count, the recent Warning events " +
	"for any that restarted, and the Services that route to each. Have the reviewer check the answer against the data."

func parseOptions(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("coordinator-chaos", flag.ContinueOnError)
	fs.StringVar(&o.kubeconfig, "kubeconfig", "", "kubeconfig file (default: kubectl's own lookup)")
	fs.StringVar(&o.kctx, "context", "", "kubectl context to test (required; the current context is never used)")
	fs.StringVar(&o.namespace, "namespace", "", "the crew's namespace (default: the crew name)")
	fs.StringVar(&o.crew, "crew", "", "the crew to ask (required)")
	fs.StringVar(&o.agent, "coordinator-agent", "", "the coordinator Agent (default: <crew>-coordinator)")
	fs.StringVar(&o.natsURL, "nats-url", "", "NATS URL; empty starts a kubectl port-forward")
	fs.StringVar(&o.natsNamespace, "nats-namespace", "nats", "namespace of the NATS service, for the port-forward")
	fs.StringVar(&o.natsService, "nats-service", "nats", "NATS service name, for the port-forward")
	fs.StringVar(&o.configName, "kubemoot-config", "default", "the KubemootConfig to compare the flags with")
	fs.StringVar(&o.debugNamespace, "signal-namespace", "kubemoot-chaos", "namespace of the signaling pod")
	fs.StringVar(&o.debugImage, "signal-image", "natsio/nats-box:0.14.5", "image with sh, grep, cut and kill for that pod")
	fs.StringVar(&o.outDir, "out", "chaos-report", "directory for report.json and report.md")
	fs.StringVar(&o.cases, "cases", "", "comma-separated name filters; empty runs all")
	fs.StringVar(&o.mode, "mode", string(coordchaos.ModeProtected), "protected (flags on) or control (flags off)")
	fs.StringVar(&o.question, "question", defaultQuestion,
		"the question asked; it must need the crew's specialists, or the coordinator answers it directly and the later kill points are never reached")
	fs.IntVar(&o.port, "agent-port", 8080, "the agents' HTTP port, for /q/metrics")
	fs.BoolVar(&o.list, "list", false, "print the matrix and exit")
	fs.BoolVar(&o.preflightOnly, "preflight", false, "check the flags and the cluster, then exit without a fault")
	fs.DurationVar(&o.pointTimeout, "point-timeout", 10*time.Minute, "safety net: wait for a kill point")
	fs.DurationVar(&o.settleTimeout, "settle-timeout", 0, "safety net after the fault (default 10m, control 3m)")
	fs.DurationVar(&o.quiet, "quiet", 10*time.Second, "watch for a late duplicate this long after the close")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	return o, o.validate()
}

func (o *options) validate() error {
	if o.kctx == "" {
		return fmt.Errorf("--context is required")
	}
	if o.crew == "" {
		return fmt.Errorf("--crew is required")
	}
	if o.namespace == "" {
		o.namespace = o.crew
	}
	if o.agent == "" {
		o.agent = o.crew + "-coordinator"
	}
	if o.settleTimeout == 0 {
		o.settleTimeout = 10 * time.Minute
		if o.mode == string(coordchaos.ModeControl) {
			o.settleTimeout = 3 * time.Minute
		}
	}
	if o.mode != string(coordchaos.ModeProtected) && o.mode != string(coordchaos.ModeControl) {
		return fmt.Errorf("--mode must be protected or control")
	}
	return nil
}

func main() {
	o, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "coordinator-chaos:", err)
		os.Exit(coordchaos.ExitRefused)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, o)
	stop()
	os.Exit(code)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, args...)...)
}

func selectCases(o options) ([]coordchaos.Case, error) {
	var filters []string
	if o.cases != "" {
		filters = strings.Split(o.cases, ",")
	}
	return coordchaos.Select(coordchaos.Matrix(coordchaos.Mode(o.mode)), filters)
}

func run(ctx context.Context, o options) int {
	cases, err := selectCases(o)
	if err != nil {
		logf("%v", err)
		return coordchaos.ExitRefused
	}
	if o.list {
		for _, c := range cases {
			fmt.Printf("%-40s replicas=%d flags=%+v attempts=%d\n", c.Name, c.Replicas, c.Need, c.Attempts)
		}
		return coordchaos.ExitOK
	}
	k := coordchaos.Kubectl{Kubeconfig: o.kubeconfig, Context: o.kctx}
	signaler := &coordchaos.NodeSignaler{K: k, Namespace: o.debugNamespace, Image: o.debugImage}
	cluster := &coordchaos.KubeCluster{K: k, Namespace: o.namespace, Agent: o.agent, Port: o.port, Signaler: signaler}
	flags, err := preflight(ctx, cluster, o, cases)
	if err != nil {
		logf("refusing to run: %v", err)
		return coordchaos.ExitRefused
	}
	if o.preflightOnly {
		logf("preflight ok: effective flags %+v", flags)
		return coordchaos.ExitOK
	}
	return execute(ctx, o, k, cluster, signaler, cases, flags)
}

// preflight reads the flags the coordinator pods run with and the KubemootConfig, and
// refuses a run the coordinator cannot support.
func preflight(ctx context.Context, cluster *coordchaos.KubeCluster, o options,
	cases []coordchaos.Case) (coordchaos.Flags, error) {
	pods, err := cluster.CoordinatorPods(ctx)
	if err != nil {
		return coordchaos.Flags{}, err
	}
	effective, err := coordchaos.EffectiveFlags(pods)
	if err != nil {
		return coordchaos.Flags{}, err
	}
	var configured *coordchaos.Flags
	if cfg, err := cluster.ConfiguredFlags(ctx, o.configName); err == nil {
		configured = &cfg
	} else {
		logf("note: KubemootConfig %s not read: %v", o.configName, err)
	}
	notes, err := coordchaos.CheckFlags(coordchaos.Mode(o.mode), coordchaos.Union(cases), effective, configured)
	for _, n := range notes {
		logf("note: %s", n)
	}
	return effective, err
}

func execute(ctx context.Context, o options, k coordchaos.Kubectl, cluster *coordchaos.KubeCluster,
	signaler *coordchaos.NodeSignaler, cases []coordchaos.Case, flags coordchaos.Flags) int {
	started := time.Now()
	original, err := cluster.Replicas(ctx)
	if err != nil {
		logf("reading the coordinator's replicas: %v", err)
		return coordchaos.ExitRefused
	}
	defer restore(cluster, signaler, original)
	url := o.natsURL
	if url != "" {
		logf("note: NATS at %s is read as given; --context %s does not bound it", url, o.kctx)
	}
	if url == "" {
		pf, err := coordchaos.StartPortForward(ctx, k, o.natsNamespace, o.natsService, 4222, time.Minute)
		if err != nil {
			logf("%v", err)
			return coordchaos.ExitRefused
		}
		defer pf.Close()
		url = pf.URL
	}
	bus, err := coordchaos.DialBus(ctx, url, o.namespace, o.crew, time.Time{})
	if err != nil {
		logf("%v", err)
		return coordchaos.ExitRefused
	}
	defer bus.Close()
	r := &coordchaos.Runner{
		Cfg: coordchaos.Config{
			Namespace: o.namespace, Crew: o.crew, PointTimeout: o.pointTimeout,
			SettleTimeout: o.settleTimeout, Quiet: o.quiet, Question: o.question,
		},
		Cluster: cluster, Bus: bus, Log: logf,
		Asker: coordchaos.GatewayAsker{K: k, Namespace: o.namespace, Crew: o.crew},
	}
	report := coordchaos.Report{
		Started: started, Context: o.kctx, Crew: o.crew, Mode: coordchaos.Mode(o.mode), Flags: flags,
		Cases: r.Run(ctx, cases), Finished: time.Now(),
	}
	if err := writeReport(o.outDir, report); err != nil {
		logf("writing the report: %v", err)
	}
	fmt.Print(report.Markdown())
	return report.ExitCode()
}

// restore puts the coordinator's replicas back, lets a frozen coordinator go, and removes
// the signaling pods. It runs even when the run was interrupted.
func restore(cluster *coordchaos.KubeCluster, signaler *coordchaos.NodeSignaler, replicas int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if pods, err := cluster.CoordinatorPods(ctx); err == nil {
		for _, err := range signaler.ContinueAll(ctx, pods) {
			logf("a coordinator may still be frozen: %v", err)
		}
	}
	if err := cluster.SetReplicas(ctx, replicas); err != nil {
		logf("restoring replicas to %d: %v", replicas, err)
	}
	if err := signaler.Cleanup(ctx); err != nil {
		logf("removing the signaling pods: %v", err)
	}
}

func writeReport(dir string, r coordchaos.Report) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.Create(dir + "/report.json") // #nosec G304 -- the operator picks the output directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := r.WriteJSON(f); err != nil {
		return err
	}
	return os.WriteFile(dir+"/report.md", []byte(r.Markdown()), 0o600)
}
