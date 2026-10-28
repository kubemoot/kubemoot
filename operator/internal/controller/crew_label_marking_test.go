package controller

import "testing"

const testHelmManager = "Helm"

func TestMarkingAChartMadeNamespaceKeepsItsManager(t *testing.T) {
	ns := nsWith("team-a", map[string]string{labelManagedBy: testHelmManager, "keep": "me"})
	markCrewLabel(ns, "c1")
	if ns.Labels[labelCrew] != "c1" || ns.Labels[labelManagedBy] != testHelmManager || !operatorSetCrewLabel(ns) {
		t.Fatalf("mark: %+v %+v", ns.Labels, ns.Annotations)
	}
	unmarkCrewLabel(ns)
	if _, ok := ns.Labels[labelCrew]; ok || operatorSetCrewLabel(ns) {
		t.Fatalf("unmark left the operator's marks: %+v %+v", ns.Labels, ns.Annotations)
	}
	if ns.Labels[labelManagedBy] != testHelmManager || ns.Labels["keep"] != "me" {
		t.Fatalf("unmark removed what it did not set: %+v", ns.Labels)
	}
}

func TestMarkingABareNamespaceLeavesNothingBehind(t *testing.T) {
	ns := nsWith("team-b", nil)
	markCrewLabel(ns, "c2")
	if ns.Labels[labelManagedBy] != managedByValue || !operatorSetCrewLabel(ns) {
		t.Fatalf("mark: %+v", ns.Labels)
	}
	unmarkCrewLabel(ns)
	if len(ns.Labels) != 0 || len(ns.Annotations) != 0 {
		t.Fatalf("unmark left marks: %+v %+v", ns.Labels, ns.Annotations)
	}
}

func TestOperatorSetCrewLabelNeedsARecord(t *testing.T) {
	if operatorSetCrewLabel(nsWith("team-c", map[string]string{labelCrew: "c3"})) {
		t.Fatal("a label nobody recorded is not the operator's")
	}
	if !operatorSetCrewLabel(nsWith("team-d", map[string]string{labelCrew: "c4", labelManagedBy: managedByValue})) {
		t.Fatal("the earlier managed-by record still counts")
	}
}
