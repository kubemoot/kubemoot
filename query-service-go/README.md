# Query service (Go)

One process per crew that answers similarity searches for every RAGSource of the crew. It
replaces the Python `query-service/`, which runs one pod per RAGSource (about 100 MiB each).
This service holds the same contract, so the agents' RAG tools, the MCP gateway's tool
search and the operator's `/info` poll keep working when `status.queryEndpoint` points at a
source under it.

The operator runs it when the KubemootConfig sets `queryService.engine: go` (the operator chart
value of the same name): one Deployment and Service per namespace that has RAGSources, named
`<namespace>-query`, with the registry in a ConfigMap mounted as a directory and each vector store
Secret mounted as a `credentialsDir`. A RAGSource's `status.queryEndpoint` is then
`http://<namespace>-query.<namespace>:<port>/sources/<name>`. See `docs/reference/ragsource-guide.md`
(Query service engines).

It embeds the question through the model server's OpenAI-compatible `/v1/embeddings`
endpoint, scales the vector to unit length, and runs a cosine query against the
`data_<collection>` table the indexer writes (pgvector, via pgx).

## Registry

The service reads a registry file (JSON, `KUBEMOOT_REGISTRY_FILE`, default
`/etc/kubemoot/query/registry.json`) that lists the sources it serves. The operator writes it
into a ConfigMap from each RAGSource's serving index. The service watches the file's
directory (a ConfigMap mount swaps a symlink there) and also rereads it every 30 seconds as a
safety net. A new file replaces the whole registry in one atomic swap; a request in flight
finishes against the registry it started with (a connection pool the new registry drops stays
available for 45 seconds, longer than the 30 second limit on a query). An invalid file, or a
new registry whose connections cannot be opened, is logged and the previous registry stays. The service starts without a registry, reports not ready, and picks it up
when it appears.

```json
{
  "sources": [
    {
      "name": "pilot-docs",
      "namespace": "pilot",
      "vectorStore": {
        "type": "pgvector",
        "endpoint": "pg.pilot:5432/vectors",
        "collection": "pilot-docs-v2",
        "dimensions": 768,
        "credentialsDir": "/etc/kubemoot/credentials/pilot-docs"
      },
      "embedding": {"endpoint": "http://vllm-embed.models:8000", "model": "nomic-embed-text"},
      "defaultTopK": 5
    }
  ]
}
```

- `name` is the URL segment (`/sources/<name>/...`): lowercase letters, digits, `.` and `-`.
- `vectorStore.endpoint` accepts `postgresql://user:pass@host:5432/db`, `host:port/db`,
  `host:port` or `host`. Missing parts default to `localhost`, `5432`, `vectors`, `postgres`.
- The file holds no secrets. Credentials come from `credentialsDir`, a mounted Secret with the
  files `password` (required) and `username` (optional), read again on every rescan so a
  rotated Secret takes effect without a restart. Without a `credentialsDir`, the user info of a
  URL endpoint is used, then `KUBEMOOT_DB_USER`, `KUBEMOOT_DB_PASSWORD` and `KUBEMOOT_DB_NAME`.
- Unknown keys are an error. A source's connection pool is shared with any other source that
  uses the same database and credentials, and pools no source uses after a swap are closed.
- The embedding API key (`KUBEMOOT_EMBEDDING_API_KEY`) is sent to every source's embedding endpoint,
  so only the operator may write the registry.
- A collection change (an index swap) is a registry change: the new collection is served from
  the swap on, and `GET /ready` lists each source's collection so a rollout can wait for it.

## Routes

| Route | Answer |
|---|---|
| `POST /sources/<name>/query` | `{query, results:[{content, metadata, score}], collection, embedding_time_ms, search_time_ms}` |
| `GET /sources/<name>/info` | source, vector store, embedding and `stats.document_count` (null when the table cannot be counted) |
| `GET /sources/<name>/health` | `{status, ragsource, vectorstore, embedding}` |
| `GET /sources/<name>/ready` | checks that source's database and embedding server; 503 names what is down |
| `POST /sources/<name>/mcp` | MCP over streamable HTTP (stateless, JSON answers) with one tool, `<name>_search(query, top_k)` |
| `GET /health` | `{status:"healthy"}`; does not depend on the registry or any backend |
| `GET /ready` | 200 once a registry is loaded, with its version and each source's collection; 503 before. A source's database being down does not make the pod unready |
| `GET /version` | `{version, service}` |
| `GET /metrics` | Prometheus text |

Request body of `/query`: `{query, top_k (1 to 100, default the source's or KUBEMOOT_DEFAULT_TOP_K),
threshold (0 to 1), filter}`. `filter` is validated and ignored, as it always was.

## Same contract as the Python service

Status codes: 404 when the table does not exist yet (`Collection '<c>' not found. Has indexing
completed?`), 502 when the embedding server fails or answers non-2xx, 500 for anything else,
all as `{"detail": "..."}`. A query that runs past 30 seconds is 504 `Query timed out` (the
Python service had no limit). A request body over 4 MiB is 413. A name the registry does not list is 404 `Source '<name>' not
found`; any other path is 404 `Not Found`, a wrong method 405 `Method Not Allowed`.

FastAPI answers an invalid request with 422 and `{"detail": [{type, loc, msg, input, ctx}]}`.
This service answers the same status and shape, with the same `type` and `loc` for: missing
body, malformed JSON, a body that is not an object, a missing or non-string `query`, `top_k`
not a whole number or outside 1 to 100, `threshold` not a number or outside 0 to 1, and a
`filter` that is not an object. Like pydantic's lax mode, a numeric string or a boolean is
accepted for `top_k` and `threshold`. `msg` follows pydantic's wording for these cases. For
malformed JSON only `type` and the first `loc` element match (the error text and offset come
from different parsers). Callers only branch on 2xx.

The MCP tool's text answer (`<passage n="1" source="..." score="0.81">`, one block per chunk)
is identical. Its error results carry `isError: true` and the text
`Error executing tool <name>_search: ...` as before; a 500's detail is never shown to the
model, only logged. Argument validation messages are shorter than pydantic's. A successful
result omits `isError` where the Python service sent `false`.

Metrics keep their names and the `ragsource` label: `kubemoot_query_requests_total`,
`kubemoot_query_success_total`, `kubemoot_query_errors_total`,
`kubemoot_embedding_time_ms_total`, `kubemoot_search_time_ms_total`. Each source's series
exists from the moment the registry lists it and goes when the registry drops it.

Logs are JSON on stderr (`LOG_LEVEL`) and never contain the question's text, only its length.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `KUBEMOOT_REGISTRY_FILE` | `/etc/kubemoot/query/registry.json` | the registry |
| `KUBEMOOT_QUERY_PORT`, `PORT` | `8000` | listen port |
| `KUBEMOOT_QUERY_HOST` | `0.0.0.0` | listen address |
| `KUBEMOOT_DEFAULT_TOP_K` | `5` | results when neither the request nor the source says; kept within 1 to 100 |
| `KUBEMOOT_EMBEDDING_API_KEY` | empty | bearer token for the embedding servers |
| `KUBEMOOT_DB_USER`, `KUBEMOOT_DB_PASSWORD`, `KUBEMOOT_DB_NAME` | | database defaults, see the registry |
| `LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARN`, `ERROR` |

## Build, test, lint

```bash
go test ./...
../.github/scripts/golangci-lint.sh
../.github/scripts/go-build-static.sh --x "github.com/kubemoot/kubemoot/query-service-go/internal/version.Version=$(git rev-parse --short=8 HEAD)" query-service-go .
```

The version is stamped at link time from the build (the commit in CI; the release workflow
retags the image with the version from the git tag), never written in source. A plain
`go build` reports `dev`. The image is the tested static binary packaged with the Paketo
Procfile buildpack (`build-go-binary-image.yaml`), like the other Go services.

The pgstore tests that need a database run when `KUBEMOOT_TEST_DATABASE_URL` names one with the
pgvector extension available (for example
`postgres://postgres@127.0.0.1:5432/vectors`); without it they skip, as CI has no pgvector.

## Compare against the Python service (`qs-compare`)

`cmd/qs-compare` replays a recorded query set against two services and reports where they
differ. The query set is JSONL, one query per line (see `testdata/queries.example.jsonl`):

```json
{"id":"ask-01","source":"pilot-docs","body":{"query":"how do I scale a deployment","top_k":5}}
{"id":"info","source":"pilot-docs","method":"GET","path":"/info"}
```

`method` defaults to POST with a body and GET without; `path` defaults to `/query`. `body` is
sent as written, so the set can include invalid requests.

```bash
go run ./cmd/qs-compare -queries set.jsonl \
  -a 'http://{source}-query.pilot:8000' \
  -b 'http://pilot-query.pilot:8000/sources/{source}'
```

`{source}` in a base URL is replaced with the query's `source`; a base without it is used as
it is, so a set can be replayed per source against a Python pod on its own port. Queries run
one at a time. Two answers match when:

- the status is the same;
- for a 200 `/query`: `query` and `collection` are equal, the result counts are equal, scores
  agree within `-tolerance` (default 1e-6) rank by rank, and content and metadata agree in
  order, except that rows whose scores tie within the tolerance are compared as a set (an
  index may return tied rows in any order). `embedding_time_ms` and `search_time_ms` are
  ignored. A tie that straddles the `top_k` cutoff can pick different members and shows as a
  difference;
- for a 404, `detail` is equal; for a 422, the `type` and `loc` of each problem are equal; for
  other errors the status is the contract;
- for any other 200 route (`/info`, `/health`), the whole body is equal.

Exit status 0 means every query matched, 1 that at least one differed, 2 that the command could
not run.
