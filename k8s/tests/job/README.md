# Test Jobs

`job-smoke.yaml` (quick, about 2 minutes) and `job-all.yaml` (the full suite, about 10
minutes) run the test scripts baked into the test-runner image as a Job in the
`kubemoot` namespace.

The Jobs run as the operator chart's `kubemoot-verify-runner` ServiceAccount
(`operator/chart/kubemoot-operator/templates/verify-rbac.yaml`), the one source of the
test runner's permissions. Install the chart with `verify.enabled=true`.

The manifests hold `test-runner:0.0.0`. Run them with `run.sh`, which resolves the latest
released test-runner from the git tags (or takes `TEST_RUNNER=X.Y.Z`) and replaces the
previous run:

```bash
git fetch --tags
k8s/tests/job/run.sh smoke
kubectl logs -f job/kubemoot-test-smoke -n kubemoot
```

The Integration Test workflow runs `job-smoke.yaml` with the test-runner candidate the
operator chart pins, from Harbor.
