---
title: "JSON Schemas for the CRDs"
description: "Every Kubemoot release publishes the JSON Schemas of its CRDs, so an editor or CI validates a crew's manifests offline, with no cluster."
weight: 20
---

## Overview

Each Kubemoot release carries the JSON Schemas of the `kubemoot.ai/v1alpha1` kinds as assets of its GitHub Release. They are generated from the same CRDs the operator installs, so they describe exactly the version of Kubemoot they are attached to.

| File | Covers |
|------|--------|
| `kubemoot_v1alpha1.json` | Every kind. Each document is checked against the schema of its `kind`; a document of another kind passes. One mapping covers a whole crew repository. |
| `crew_v1alpha1.json`, `agent_v1alpha1.json`, `promptmodule_v1alpha1.json`, ... | One kind each, named for the lower-case kind. |

Download them from the release of the version you run, or follow the newest release:

```
https://github.com/kubemoot/kubemoot/releases/download/<release tag>/kubemoot_v1alpha1.json
https://github.com/kubemoot/kubemoot/releases/latest/download/kubemoot_v1alpha1.json
```

The schemas close every object that lists its fields, so a misspelled field is an error, and they carry the field descriptions for hover help. They check structure. Rules the API server checks with CEL, and anything that needs the cluster, stay with the server: a server-side dry run (`kubectl apply --dry-run=server`) covers those.

## In VS Code

`kmctl create` writes a `.vscode/settings.json` that maps the crew's manifests to the combined schema for the Red Hat YAML extension, and a `.vscode/extensions.json` that recommends the extension. Files under `templates/` are Helm templates, so they are associated with the Helm language and are not checked as plain YAML (CrewForge lints them after rendering).

To validate against a mirror, for instance on an air-gapped network, pass `kmctl create --schema-url <url>`.

## In CI

Any JSON Schema validator reads them. For example, with `check-jsonschema`:

```sh
check-jsonschema --schemafile kubemoot_v1alpha1.json crew.yaml agents.yaml promptmodules.yaml
```

## Generating them

`go run ./cmd/crd-schema -crds config/crd/bases -out <dir>` in `operator/` writes the same files from the CRDs in a checkout. The release does this for its own tag; nothing about the schemas is versioned in git.
