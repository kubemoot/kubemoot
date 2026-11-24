---
title: "Encrypted calls that carry credentials"
weight: 58
description: "Which calls inside the cluster carry a bearer token, how each is protected with TLS, and what to set so an install does not depend on the cluster encrypting pod traffic."
---

Kubemoot does not assume the cluster encrypts traffic between pods. A call inside the
cluster that carries a bearer token therefore uses TLS, verified against a CA the install owns.
This page describes that CA and lists the calls it protects.

## The install CA

The operator keeps one CA for the install, a Secret in its namespace (`global.installCA.secret`, default
`kubemoot-ca`).

- **Creation.** The operator makes the Secret when it is absent and never replaces one that
  exists, so no `helm template`, Argo CD render or upgrade can rotate the CA. The chart makes no CA.
  The CA lasts ten years.
- **What it signs.** The serving certificates of the services that carry tokens. Each is signed
  for the Service names clients use, ends no later than the CA does, and is renewed once a third
  of its year is left.
- **Publishing.** The operator publishes the CA's public certificates in a ConfigMap in its
  namespace (`global.installCA.configMap`) and compares it with the Secret every minute. The dashboard and
  the crew liaison mount that ConfigMap and verify what they connect to against it. No pod
  mounts the CA Secret, which holds the key.
- **Rotation.** Delete the Secret and the operator makes a new CA. The ConfigMap then holds both
  the new and the previous CA, so clients trust certificates of either, until every certificate the
  previous CA signed has been replaced; then the previous CA is dropped.
- **A CA near its end.** A CA that has expired or has under 30 days left signs nothing, and
  the operator says so in its log. Delete the Secret to replace it.

## The operator's report service

The report service (port 8082 of `<release>-kubemoot-operator-report`) answers the conversation
export, the crew memory export, the fitness report and the component status. Every request
carries a bearer token, so the service serves TLS and nothing else: there is no plain listener. If it has
no certificate source (no install CA, or no `REPORT_SERVICE_NAME`), it refuses to start rather than fall back
to plain http; `REPORT_ALLOW_INSECURE=true` overrides that for a development run outside the chart, and the chart
never sets it.

- **Certificate.** The operator signs the certificate in memory from the install CA for the
  Service's names and replaces it when it is no longer fresh or the CA changes. No Secret, and no key on
  disk, holds it. If the CA cannot be read for a moment, the certificate the operator has is served
  while it is valid.
- **Callers.** The crew liaison (`export_conversation`) and the dashboard verify the certificate against the
  mounted CA bundle before they send the token. Both read the bundle again when it changes, so a rotation reaches
  them without a restart. The dashboard uses the CA for these calls only: it does not trust it for anything
  else it connects to.
- **The liaison keeps working on every install.** It needs neither `reportsAPI` nor an opt-in, and
  a person verified by a crew's service token can still export, because the report service
  checks that token itself.
- **Other clients.** A client that reached the report port over plain http (a script in a pod, the
  API server's service proxy used by `kmctl` before 0.22.0) must use https and trust the CA. `kmctl`
  0.22.0 and later (kmctl #76) reaches the report service through the aggregated API
  (`reportsAPI.enabled`) with the caller's own kubeconfig, which needs no CA.
- **Upgrading.** The operator, dashboard and liaison move together with the chart. While the operator
  pod rolls, a dashboard or liaison that is already on https gets connection failures from the old
  plain operator, and succeeds once the new one is ready. The CA name is one value for the whole
  install, `global.installCA.configMap` (default `kubemoot-ca`), which the operator publishes to and the
  dashboard and liaison mount; a dashboard installed from its own chart uses its `installCA.configMap`,
  which has the same default. A dashboard that is not given the CA cannot verify the report service
  and its report calls fail.
