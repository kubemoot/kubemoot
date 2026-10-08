# code-sandbox

An MCP stdio server that runs short programs for an agent: the compute tooler
writes Python or bash, the sandbox runs it, and the agent gets stdout, stderr,
and the exit code back. Counting, sorting, and arithmetic come from running
code, not from the model reading a list.

The pod is the sandbox. It holds no credentials, the crew's network policy
limits its egress, and the artifact-access materializer sidecar stages the data
a program reads under `/artifacts`.

## Why a sandbox

Language models are unreliable at counting, summing, sorting, and filtering,
especially small local models reading long lists. Asked how many pods are
running in each namespace over a long listing, a model gives a confident,
plausible, often wrong number.

In a crew, that work goes to the compute agent. Another agent gathers the raw
data into the crew's artifact store; the compute agent writes a short Python or
bash program that reads it from `/artifacts` and computes the answer; the
sandbox runs it and returns the exact output; the compute agent reports that
result, and the coordinator uses it. The runtime enforces the contract: the
compute agent may not state a number that did not come from running code.

Because it runs model-written code, the sandbox is locked down: no credentials
and no Kubernetes access, since it only computes over data already handed to
it, and no network beyond what the crew's network policy allows its
artifact-staging sidecar. See "How a program runs" below for how each run is
kept short-lived and contained. The pod itself is the sandbox, so a bad program
can at worst waste that pod's CPU until the time limit ends it.

The models decide what to compute; the sandbox makes the numbers real.

## Tools

| tool | input | result |
|------|-------|--------|
| `execute_code` | `language` (`python` or `bash`), `code`, optional `filename` | stdout, stderr, exit code, execution time |
| `validate_code` | same | whether the syntax is valid, and the errors if not |

## How a program runs

- In a new temporary directory, which is also its `HOME`, removed afterwards.
- With no stdin: a read returns end of input at once, so a program that expects
  piped data fails fast instead of waiting. Data goes in the code or in a file
  under `/artifacts`.
- In its own process group. At the time limit the whole group is killed, and
  anything a finished program left running in the background is killed too.
- With stdout and stderr each capped; the result says when output was cut.
- A program that fails is a normal result with its exit code; only an unknown
  language or a program that cannot start is a tool error.

The image is `python:3.14-slim` plus this binary and `tini`: Python with the
standard library only, and bash. The server runs under `tini -s`, which reaps
the processes a killed program leaves behind; start it the same way when you
give it an explicit command (`/usr/bin/tini -s -- /usr/local/bin/code-sandbox`).

## Config (env)

| var | default | meaning |
|-----|---------|---------|
| `EXECUTION_TIMEOUT` | `30` | seconds a program may run |
| `MAX_OUTPUT_BYTES` | `65536` | cap on each of stdout and stderr |
| `SANDBOX_TMPDIR` | OS default | where run directories are created |

## Test

```bash
go test ./...
```

The tests run real `python3` and `bash`, so both must be on `PATH`.
