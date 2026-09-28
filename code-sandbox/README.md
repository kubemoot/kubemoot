# code-sandbox

An MCP stdio server that runs short programs for an agent: the compute tooler
writes Python or bash, the sandbox runs it, and the agent gets stdout, stderr,
and the exit code back. Counting, sorting, and arithmetic come from running
code, not from the model reading a list.

The pod is the sandbox. It holds no credentials, the crew's network policy
limits its egress, and the artifact-access materializer sidecar stages the data
a program reads under `/artifacts`.

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

The image is `python:3.12-slim` plus this binary: Python with the standard
library only, and bash.

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
