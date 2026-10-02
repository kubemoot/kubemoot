---
title: "11. Kubemoot Naming Conventions"
weight: 11
---

Date: 2025-12-01

## Status

Accepted

## Context

A Kubernetes operator with a dozen CRDs and supporting components needs naming conventions that survive the project's growth and read cleanly in `kubectl` output, Go source, Helm templates, and prose. Without convention, drift is inevitable: `kubemoot-operator` here, `kubemootOperator` there, `KUBEMOOT_OPERATOR` somewhere else, all referring to the same thing.

## Decision

We will follow these conventions:

| Item | Convention | Example |
|------|------------|---------|
| API Group | `kubemoot.ai` | `apiVersion: kubemoot.ai/v1alpha1` |
| Operator namespace | `kubemoot` | The operator deployment lives here |
| Operator name | `kubemoot` (no `-operator` suffix) | `kubectl get deploy -n kubemoot kubemoot-operator` is one component; the project itself is `kubemoot` |
| CRD names | Singular, CamelCase | `ModelProvider`, `Agent`, `CrewSchedulingPolicy` |
| Short names | Lowercase abbreviations | `mdlp`, `mdl`, `mcp`, `rag`, `csp`, `moot`, `pm`, `wc` |
| Brand names in prose | CrewForge, Homelab Pilot, Kubemoot | Use the brand names in prose and UI text; use the technical identifiers in code and config |
| Brand names in code/config | `vscode-crewforge`, `homelab-pilot`, `kubemoot` | Kebab-case for everything machine-read |
| Hedge prefixes | Forbidden | No "honest", "honestly", "frankly"; state every claim directly |

## Consequences

- `kubectl get` results are scannable; short names match the convention from upstream resources (`po`, `svc`, `ing`).
- The Go API package is unambiguous: `import kubemootv1alpha1 "github.com/.../api/v1alpha1"`.
- Helm chart templates, Flux HelmReleases, and dashboard UI use the same kebab-case identifiers; copy-paste across layers works.
- Prose and UI consistency lowers reader friction across docs, the dashboard, and conference talks.
- Adding a new CRD requires picking both a CamelCase name and a short-name abbreviation; this is a 30-second decision but it must happen at design time, not at first `kubectl get` failure.

## References

- [Kubernetes API Conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md)
