# code-sandbox

An MCP server, over stdio or Streamable HTTP, that runs short programs for an agent: the compute tooler
writes Python or bash, the sandbox runs it, and the agent gets stdout, stderr,
and the exit code back. Counting, sorting, and arithmetic come from running
code, not from the model reading a list.

The pod is the sandbox. It holds no credentials, the crew's network policy
limits its egress, and the workspace-access materializer sidecar stages the data
a program reads under `/kubemoot/workspace`.

## Why a sandbox

The concepts page, [Code sandbox](https://kubemoot.org/docs/concepts/code-sandbox/),
explains the why, the compute contract, and how to run the pod under a sandboxed
runtime with `spec.runtimeClassName`. This README is the component reference.

Language models are unreliable at counting, summing, sorting, and filtering,
especially small local models reading long lists. Asked how many pods are
running in each namespace over a long listing, a model gives a confident,
plausible, often wrong number.

In a crew, that work goes to the compute agent. Another agent gathers the raw
data into the crew's artifact store; the compute agent writes a short Python or
bash program that reads it from `/kubemoot/workspace` and computes the answer; the
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
| `execute_code` | `language` (`python` or `bash`), `code`, optional `filename`, optional `inputs` | stdout, stderr, exit code, execution time |
| `validate_code` | same | whether the syntax is valid, and the errors if not |

The `execute_code` description tells the model that a program cannot fetch data,
so the model looks to other agents for it, and that it can read a stored result of its discussion in full by key.

### Inputs: a stored result by key

`inputs` lists keys of the calling discussion's stored results, `{agent}/{nnn}-{tool}` as the discussion's list of
stored results shows them. Each becomes the file `./inputs/{agent}/{nnn}-{tool}`, a copy of the whole result, so a
program counts over every row. The discussion comes from the call's `_meta.workspacePrefix`
(`{namespace}/{crew}/{thread}/`, set by the agent runtime and relayed by the gateway), never from the key. A key of
another discussion, an unknown key, a pattern, more than 32 keys, or any key on a call with no discussion is refused
before the program runs (`internal/inputs`). Its prefix rules are the gateway's and workspace-access's, checked against one shared file of vectors, `test/fixtures/workspace-prefix-vectors.json`, that all three run.

### What a program sees of the workspace

The workspace volume holds every discussion's results and a program runs as the server's user, so each program
is started in its own user and mount namespace (`internal/confine`, through a re-executed copy of this binary):

- `/kubemoot/workspace` and the old `/artifacts` show only the calling discussion's folder, read-only (a program
  cannot plant a link or alter a result), or an empty directory for a call with no discussion.
- The run directory's parent, the temporary root, is a private tmpfs holding only that run's directory, so
  concurrent programs cannot read each other's inputs.
- No capabilities remain, so no mount can be undone (umount, a read-write remount, and a nested user namespace
  that remounts are tested), and the program runs in its own pid namespace with a fresh `/proc`, so the server and
  other runs are not listed. A host that refuses the `/proc` mount keeps the old one, where the kernel's ptrace
  rules deny every entry of a process in another user namespace.
- `internal/inputs` opens each input once, every path component relative to the one before it with
  `O_NOFOLLOW`, so a symbolic link anywhere in the path (also the thread's folder) is refused and the file that
  was checked is the file that is copied. It takes regular files only and caps an input at 64 MiB and a call's
  inputs at 256 MiB. A thread folder that is a link, or has a link in its path, shows the program an empty workspace.
- The private temporary directory is a tmpfs of `SANDBOX_TMP_SIZE` (default `256m`). Its pages count against the
  pod's memory limit while a run holds them, so the worst case is the size times the concurrent runs. The run's own
  directory, where the program starts, is on the pod's disk and is not size-capped: bound it with the pod's
  ephemeral-storage limit.

This needs user namespaces and mounts. gVisor allows them (checked on the homelab's gVisor). A plain runtime with the
default `RuntimeDefault` seccomp profile blocks them, and then every `execute_code` call fails with an error that
names the cause until `SANDBOX_CONFINEMENT=off`. Off means no isolation between discussions or between concurrent
runs. A helper failure is a tool error, never a program exit code. At startup only, the server refuses to start when a
volume whose mount path is named `workspace` or `artifacts` is mounted somewhere it does not hide (it finds
volumes by that name, not by content; a volume mounted under another name is not found): list the path in
`SANDBOX_WORKSPACE_DIRS`.

## How a program runs

- In a new temporary directory, which is also its `HOME`, removed afterwards.
- With no stdin: a read returns end of input at once, so a program that expects
  piped data fails fast instead of waiting. Data goes in the code or in a file
  named in `inputs`, or under `/kubemoot/workspace` (the calling discussion's files).
- In its own process group. At the time limit the whole group is killed, and
  anything a finished program left running in the background is killed too.
- With stdout and stderr each capped; the result says when output was cut.
- A program that fails is a normal result with its exit code; only an unknown
  language or a program that cannot start is a tool error.

The image is built with Paketo buildpacks (`image/`): the tested binary on Paketo Python 3.14
(`BP_CPYTHON_VERSION` in `image/buildpacks.env`) with `tini`. Sandboxed programs find Python with
the standard library only, bash, and GNU date with the zone database; `internal/runner/runtime_contract_test.go`
states that. The server runs under `tini -s`, which reaps the processes a killed program leaves
behind. The image's launcher starts it: leave the pod's `command` unset. A `stdio` server that the
bridge starts names the launcher, `/cnb/process/web`. The image runs as the run image's user,
1002:1001, from `/workspace`. The pod runs it as 1000:1000, the workspace-access materializer's user: the operator's strict security mode does, and in the default relaxed mode a crew pins it with `spec.securityContext` (`runAsUser: 1000`, `runAsGroup: 1000`), as the crews' charts do.

### What the image guarantees, and how it is checked

The Dockerfile proved `bash`, GNU `date` and the zone database inside the image at build time.
A buildpacks build has no such step, and this repository has no in-cluster smoke run of a built
image for any component, so the checks are in two parts:

- `internal/runner/runtime_contract_test.go` runs the contract commands (Python minor version,
  no packages beyond pip tooling, standard library modules, `TZ=Asia/Tokyo date -d ...` zone
  conversion and date arithmetic) on the CI runner, against the Python that `image/buildpacks.env`
  pins. It also checks the Procfile and that the CI workflow builds on the `run-base` run image.
- The `run-base` image (Paketo Ubuntu Noble run, pinned by digest in kubemoot/release-actions)
  supplies bash, GNU coreutils (`date`), `tzdata` with the zone database, `ca-certificates` and
  OpenSSL. Its package list at the pinned digest was read to confirm this. A new `run-base`
  digest that drops one of these would not fail a test before the first run of an image built
  on it; the crew's tool smoke (`smokeInputs`) lists the tools but does not assert output.

The Python interpreter and tini come from the buildpacks, not from the run image: the Paketo
`cpython` buildpack installs Python, and `BP_LAUNCH_WITH_TINI=true` makes the Python start
buildpack require the `tini` buildpack, which puts tini on `PATH` at launch. The Procfile's
process replaces the start buildpack's own `tini -g -- python` process, so tini wraps the server
once.

## Config (env)

| var | default | meaning |
|-----|---------|---------|
| `EXECUTION_TIMEOUT` | `30` | seconds a program may run |
| `MAX_OUTPUT_BYTES` | `65536` | cap on each of stdout and stderr |
| `SANDBOX_TMPDIR` | OS default | where run directories are created |
| `SANDBOX_CONFINEMENT` | on | `off` removes all isolation between discussions and runs; use it only where the pod's workspace holds one discussion's data, or the runtime blocks user namespaces |
| `SANDBOX_TMP_SIZE` | `256m` | size of each run's private temporary directory (tmpfs, in memory) |
| `SANDBOX_WORKSPACE_DIRS` | empty | more workspace mount paths to hide, comma separated |
| `WORKSPACE_DIR` | `/kubemoot/workspace` | the workspace mount a program is kept out of (`ARTIFACT_DIR` is read too; `/artifacts` is always included) |
| `MCP_TRANSPORT` | `stdio` | `stdio` or `http`; any other value stops the server at start |
| `PORT` | `3000` | the listening port, in `http` mode only; a value that is not a number from 1 to 65535 stops the server at start |

## Transports

| transport | how the server is reached | when to use it |
|-----------|---------------------------|----------------|
| `http` | MCP Streamable HTTP at `/mcp` on `PORT`; `/healthz` and `/readyz` answer 200 | Always, for new installs. Required under a sandboxed runtime such as gVisor. |
| `stdio` (default) | the operator's mcp-bridge sidecar talks to the server over named pipes | Existing deployments; nothing to change. |

Under a sandboxed runtime such as gVisor, a named pipe in an `emptyDir` shared between
containers is not visible across them, so the bridged stdio sandbox crash-loops. In `http`
mode there is no bridge and no pipe: the server listens itself.

For an MCPServer, set `transport: http`, `port` to the same value as `PORT`, and the
environment variable `MCP_TRANSPORT=http`. The operator then adds no bridge sidecar, creates a
Service on `port`, and registers `http://<name>.<namespace>:<port>` with the gateway, which
calls `/mcp`. Set `healthPath: /healthz` and `readinessPath: /readyz` for HTTP probes; without
them the operator probes the TCP port. The container then runs the image entrypoint (or the
MCPServer's `command`); the workspace-access sidecar and the `/kubemoot/workspace` volume work the same.

## Test

```bash
go test ./...
```

The tests run real `python3` and `bash`, so both must be on `PATH`.
