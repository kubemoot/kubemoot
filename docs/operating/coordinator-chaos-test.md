---
title: "Coordinator Chaos Test"
description: "A repeatable test that kills or freezes a crew's coordinator at chosen points of a discussion and checks that the question was still answered once. How to run it, what each case proves, and how to read the report."
weight: 7
---

The [coordinator log](../../concepts/signals-and-protocol/#the-coordinator-log), [resume](../../concepts/signals-and-protocol/#resume)
and [leases](../../concepts/signals-and-protocol/#leases-and-more-than-one-replica) exist so that a crew's coordinator can die in the
middle of a discussion without losing the question. This test checks that claim against a running cluster. It asks a crew a real
question, waits until the discussion has reached a chosen point, kills or freezes the coordinator there, and then reads the
coordinator log and the discussion stream to see what happened.

It is a test tool. It never runs during normal operation and it changes nothing about how a crew deliberates. The only things it
writes are a question to the crew and the fault.

## What it does

For each case the driver, `coordinator-chaos` (source in `operator/cmd/coordinator-chaos`, logic in `operator/internal/coordchaos`):

1. Sets the coordinator's replica count for the case and waits until that many pods are Ready.
2. Asks the crew a question through the discussion gateway, the path `kmctl conversation ask` uses.
3. Reads the thread's coordinator log events and discussion messages until the thread sits at the case's kill point.
4. Injects the fault.
5. Waits, on state, until the thread is closed in the log and the asker has its answer, then watches a short quiet window for a late duplicate.
6. Reads the final state, runs the checks, and records how long recovery took.

Every wait is on state: a log event, a stream message, a pod condition. The timeouts are safety nets and are never the normal path. A
coordinator pod in `CrashLoopBackOff` or `ImagePullBackOff`, or an error on the asker's stream, ends the wait at once as an `error`.

The driver names its target explicitly. It needs `--context` and never follows the current kubectl context, and it refuses to run
against a coordinator whose protections are not on (below).

## Kill points

A kill point is a predicate on what the driver has read about the thread, not a delay. It holds while the thread sits at the point and
stops holding when the coordinator takes the next step, so the same predicate that says "reached" also says, after the coordinator is
dead, "the fault landed here".

| Point | Holds while | Window |
|---|---|---|
| `thread_opened` | `thread_opened` is logged and no subcommittee is logged: the advisory step is running | a model call |
| `subcommittee_convened` | the brief is logged and evaluation has not started: the brief may or may not be published | milliseconds |
| `evaluation_started` | the roster is logged and no phase has settled: the specialists are working | a model call |
| `review_started` | the review roster is logged and synthesis has not started (only a crew with analysts reaches it) | a model call |
| `synthesis_started` | the synthesis call began and the answer is not logged | a model call |
| `synthesis_done` | the answer is logged and the `synthesis` message is not published | milliseconds |
| `thread_close` | `thread_close` is published and `thread_closed` is not logged | milliseconds |

After the fault the driver waits until the coordinator can write no more (the killed pod has left the pod list, or the freeze has
been sent, plus a short observation window), reads the streams, and tests the same predicate again. If the thread still sits at the
point the trial is `landed`. If the coordinator got past the point before it stopped, the trial is not landed and the driver tries
again with a new question, up to the case's attempt limit (2 for a wide window, 5 for a narrow one).

The narrow points are reached by watching, so the fault can arrive after the next write. A case that never lands is reported as
`not_reached`, never as a pass.

## Faults

| Action | What it does | Replicas |
|---|---|---|
| `kill` | Force-deletes the coordinator pod with a grace period of 0, so the container gets SIGKILL and no shutdown hook runs. The crash case. | 1 |
| `stop` | Freezes the lease holder with SIGSTOP. When another replica has taken the thread over (`coordinator_resumed` is in the log), sends SIGCONT. The frozen coordinator wakes holding a thread it no longer owns: the zombie case. | 2 |
| `kill_holder` | Reads the thread's lease from the `kubemoot_coordinator_leases` bucket and force-deletes that pod while another replica runs. | 2 |
| none | The `baseline` case injects nothing. It proves the checks pass on a healthy run. | 1 |

### How SIGSTOP reaches a distroless pod

The coordinator image has no shell, so there is nothing to exec into. An ephemeral container (`kubectl debug --target`) does not help:
it joins the coordinator's PID namespace, where the agent is process 1, and Linux drops a signal that a member of a PID namespace
sends to that namespace's init process unless init installed a handler for it. SIGSTOP is never delivered that way. A signal from an
ancestor namespace is delivered.

So the driver creates one privileged pod with `hostPID: true` on the coordinator's node, in a namespace it labels
`pod-security.kubernetes.io/enforce=privileged` (`--signal-namespace`, default `kubemoot-chaos`). That pod runs the
`natsio/nats-box` image the chart already pins (`--signal-image`), finds the container's processes by the container id in their
cgroup, and sends the signal from the node's PID namespace:

```sh
pids=$(grep -l <container-id> /proc/[0-9]*/cgroup | cut -d/ -f3); kill -STOP $pids
```

This works on any node whose runtime puts the container id in the cgroup path, which containerd and CRI-O do, on Talos and on kind. The
signaling pod has an `activeDeadlineSeconds` and is deleted, with its namespace, when the run ends. The driver refuses a
`--signal-namespace` that already exists and was not created by the test. On exit, including after an interrupt, it sends SIGCONT to
every coordinator pod and logs any failure to do so; if you see that message, check the coordinator pods for a frozen process.

## Protections the test requires

The coordinator must run with `coordinator.eventLog` and `coordinator.resume` on, and with `coordinator.lease` on for the `stop` and
`kill_holder` cases. The driver reads the environment of the running coordinator pods (`KUBEMOOT_COORDINATOR_EVENT_LOG`,
`KUBEMOOT_COORDINATOR_RESUME`, `KUBEMOOT_COORDINATOR_LEASE`), because that is what the process reads and an Agent's own
`spec.deployment.env` wins over the chart. It also reads the KubemootConfig and notes a difference. If a needed flag is not true on
every coordinator pod it refuses to run, so a pass always means a protected coordinator survived.

`--mode control` is the opposite run. It requires all three flags off and kills an unprotected coordinator at the points a message can
show (`thread_opened`, `evaluation_started`, `synthesis_started`). Its result documents today's loss. A control case that loses the
question is `expected_loss`. It is not a pass and it does not fail the run. A control case that loses nothing is `control_no_loss`,
which says the control proved nothing for that point.

## Checks

Every check reads state: the coordinator log, the discussion stream, the asker's stream, and the coordinators' counters. None asks an
agent anything.

| Check | Fails when |
|---|---|
| `one_thread_per_question` | the question started more than one thread |
| `one_answer_on_bus` | the discussion stream holds other than one `synthesis` message for the question |
| `one_answer_to_asker` | the asker's stream delivered other than one answer |
| `one_thread_close`, `one_thread_closed_event` | other than one `thread_close` message, or one `thread_closed` event per thread |
| `duplicate_message`, `duplicate_message_id` | (a republish with the same id inside the stream's two-minute duplicate window is dropped by the stream and is not visible here) a singleton coordinator message (`advisory`, `subcommittee`, `advisory_ready`, `review_ready`, `synthesis`, `thread_close`) appears with two different ids, or the same id twice for `advisory`, `synthesis` and `thread_close` |
| `closed_reason`, `closed_reason_matches_answer` | `thread_closed.reason` is not an outcome of `synthesis_done` (`synthesized`, `capacity`, `direct`, `fallback`), or differs from the logged outcome. `zombie`, `expired`, `unlogged`, `no_signals` and `ceiling` all fail |
| `coordinator_resumed` | the thread logged `coordinator_resumed` other than exactly once after a fault, or at all on the baseline |
| `unique_event_ids` | an `eventId` is in the log twice |
| `thread_left_open` | a thread of the question is still open in the log after the trial settled |
| `coordinator_metrics` | during the trial a coordinator counted a close-check mismatch (`kubemoot_coordinator_log_checks_total{result="mismatch"}`), an unlogged thread, or a failed append |

The counters are read from `/q/metrics` of each coordinator pod through the API server and compared before and after. A pod that was
killed takes its counters with it, so the counters speak for the pods that survived or were started during the trial. The `fenced`
counter is recorded in the report as evidence of a fenced zombie and is not a pass or fail condition on its own.

Time to recover is measured on the driver's clock from the moment the fault went in to the `synthesis` message reaching the discussion
stream (`answerSeconds`) and to `thread_closed` reaching the log (`closedSeconds`).

## Outcomes

| Outcome | Meaning |
|---|---|
| `pass` | the fault landed on the point and every check held |
| `fail` | a check failed, in any trial of the case |
| `not_reached` | no attempt landed the fault on the point, so nothing was proven |
| `error` | the harness or the cluster failed (a pod that cannot start, NATS unreachable). The case says nothing about the coordinator |
| `expected_loss` | control mode: the fault landed and the thread lost what the protections keep |
| `control_no_loss` | control mode: the fault landed and nothing was lost |

The driver exits 0 when no case failed or errored and none was `not_reached`, 1 when a case failed or errored, 3 when every case
passed but one was `not_reached`, and 2 when it refused to start.

## Running it

### On a throwaway kind cluster

```sh
test/chaos/coordinator/kind.sh
```

The script creates a kind cluster with its own kubeconfig in a temporary directory (the default kubeconfig is never touched), installs
Kubemoot and the quickstart `hello` crew, turns the three flags on, runs the protected matrix, turns them off, runs the control matrix,
and deletes the cluster. Set `KUBEMOOT_IMAGE_TAG` to use images you loaded with `kind load docker-image`, `CHAOS_CASES` to run part of
the matrix, and `KEEP_CLUSTER=1` to keep the cluster. The images must contain the coordinator log, resume and lease code.

### On another cluster

Use a scratch crew, not one people rely on: the test patches the coordinator Agent's replicas and restores them when it ends.

```sh
cd operator && go build -o coordinator-chaos ./cmd/coordinator-chaos

# turn the protections on (chart values), then check them without injecting anything
helm upgrade kubemoot-operator ... --reuse-values \
  --set coordinator.eventLog=true --set coordinator.resume=true --set coordinator.lease=true
./coordinator-chaos --context <context> --crew <crew> --namespace <crew-namespace> --preflight

./coordinator-chaos --context <context> --crew <crew> --namespace <crew-namespace> --out chaos-report/protected
```

`--list` prints the matrix, `--cases` takes comma-separated name filters (`--cases kill@,baseline`), and `--nats-url` skips the
`kubectl port-forward` the driver otherwise starts to the `nats` service in the `nats` namespace. The NATS address is read as given,
so `--context` does not bound it: a `--nats-url` for another cluster reads that cluster's streams while the faults go to the context.
The test patches the coordinator Agent's replicas, so do not run it against an Agent that a GitOps controller owns. The matrix has 13 protected cases
(the baseline, a kill at each of the seven points, a freeze at two points, a lease-holder kill at three) and 3 control cases.

A trial takes as long as the crew takes to answer, plus the recovery, plus the quiet window (10 seconds by default). Scaling to two
replicas for the lease cases adds about a minute each time. Plan on an hour or more for the protected matrix on a crew whose answer
takes a minute.

### Choosing the question

The question must need the crew's specialists. A coordinator answers a simple question
directly (it convenes no subcommittee for it), so the discussion never reaches evaluation,
review or synthesis, and those kill points end `not_reached`. The default question asks a
starter crew for pods, restarts, events and Services and has its reviewer check the answer.
Pass `--question` for a crew whose specialists cover something else.

## Reading the report

Each run writes `report.json` and `report.md` to `--out`. The markdown has one row per case:

```
| Case | Replicas | Flags | Outcome | Trials | Answer s | Closed s | Violations |
| kill@synthesis_started | 1 | log+resume | pass | 1 | 41.2 | 43.0 | 0 |
```

`Trials` is how many questions it took to land the fault. Below the table, every violation is listed with the evidence that shows it
(the message ids, the event sequence). The JSON has the same data per trial: whether the fault landed, the thread ids, the collapsed
sequence of log event types (`log`), the counter deltas, and notes such as "the coordinator passed synthesis_done before it stopped".
Open the `log` of a failing trial first: it shows where the thread stopped and what the next coordinator did.

## Limits

- The narrow points (`subcommittee_convened`, `synthesis_done`, `thread_close`) last
  milliseconds. The test watches for them rather than pausing the coordinator there, so a
  run may end them `not_reached`; the restart-from-every-prefix unit tests cover them.

- The narrow points depend on how fast the driver reacts. The driver reads a stream and asks the API server to act, so the fault
  arrives some milliseconds to a few hundred milliseconds after the event it waited for. Those cases may need several trials or end
  `not_reached`.
- One question is in flight at a time, as the coordinator takes one request per crew at a time. Run nothing else against the crew
  during a run: a thread the test did not start would not be checked, but it would share the coordinator.
- The test checks the coordinator's behavior on a crew that answers. It does not test a crew's answer quality.
