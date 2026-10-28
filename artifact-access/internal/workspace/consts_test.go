package workspace

// Values the tests repeat.
const (
	selectOne     = "SELECT 1"
	tblPods       = "pods"
	jsonOneA      = `[{"a":1}]`
	jsonTwoA      = `[{"a":1},{"a":2}]`
	podsKey       = "ns/crew/t/agent/pods-1"
	artifactK     = "/artifacts/k"
	mem64         = "64MB"
	csvAB         = "a,b\n1,2\n"
	mustBeLetters = "must be letters"
	badKey        = "bad key"
)

const kubectlPods = `NAMESPACE     NAME          READY   STATUS    RESTARTS   AGE
default       web-1         1/1     Running   0          5d
default       web-2         0/1     Pending   0          2m
kube-system   dns-abc-123   1/1     Running   3          40d
`

const podsYAML = `apiVersion: v1
kind: List
items:
  - metadata:
      name: a
    status:
      phase: Running
  - metadata:
      name: b
    status:
      phase: Pending
`
