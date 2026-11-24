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
