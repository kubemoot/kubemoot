/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_tier.go resolves spec.tier of a CrewFitnessSuite into scripts. A crew
// ships its scenarios as one ConfigMap, <crew>-fitness, one key per scenario
// (<name>.adl). A scenario names the tiers it belongs to with an ADL constant,
// DEFINE CONST TIERS AS "smoke standard", so the tier travels with the scenario
// and is reviewed in the same change. Full is every scenario and needs no label.
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

// fitnessConfigMapSuffix is appended to the crew name to name the ConfigMap that
// holds the crew's scenarios. The crew chart's fitness-scenarios template owns the
// name; keep the two in step. Resolved scripts are always inline (testContent), so
// no later lookup depends on the key's extension.
const fitnessConfigMapSuffix = "-fitness"

// scenarioExtensions are the file extensions of a scenario key, as the crew chart
// ships them; README.md and any other key is not a scenario.
var scenarioExtensions = []string{".adl", ".md"}

// labelledTiers are the tier names a scenario may carry in its TIERS constant.
// Full is not one of them: every scenario is in it.
var labelledTiers = []kubemootv1alpha1.FitnessTier{
	kubemootv1alpha1.FitnessTierSmoke,
	kubemootv1alpha1.FitnessTierStandard,
}

// fitnessConfigMapName is the ConfigMap that holds a crew's scenarios.
func fitnessConfigMapName(crew string) string { return crew + fitnessConfigMapSuffix }

// scenarioTestRef is the scenario name a ConfigMap key stands for: the key
// without its extension. ok is false for a key that is not a scenario.
func scenarioTestRef(key string) (string, bool) {
	if strings.EqualFold(key, "README.md") {
		return "", false
	}
	for _, ext := range scenarioExtensions {
		if name, found := strings.CutSuffix(key, ext); found && name != "" {
			return name, true
		}
	}
	return "", false
}

// scenarioTiers reads the tiers a scenario names in its TIERS constant, in the
// order written, lower-cased. Words may be separated by spaces or commas.
func scenarioTiers(content string) []string {
	value := fitnessscript.ParseFitnessTest(content).Constants[kubemootv1alpha1.TierScenariosConstant]
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == ' ' || r == ',' || r == '\t'
	})
}

// isLabelledTier reports whether name is a tier a scenario may carry.
func isLabelledTier(name string) bool {
	for _, t := range labelledTiers {
		if string(t) == name {
			return true
		}
	}
	return false
}

// tierSelection is what a tier resolved to.
type tierSelection struct {
	scripts   []kubemootv1alpha1.SuiteScript
	available int
}

// selectTier picks the scenarios of the ConfigMap data that belong to tier, in name
// order, each as an inline script. Full takes every scenario. A scenario that names a
// tier this operator does not know is an error, not a silent omission: a typo in a
// label must not shrink a tier. A tier no scenario carries is an error too.
func selectTier(tier kubemootv1alpha1.FitnessTier, data map[string]string) (tierSelection, error) {
	keys := make([]string, 0, len(data))
	for k := range data {
		if _, ok := scenarioTestRef(k); ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return tierSelection{}, fmt.Errorf("the crew's scenarios hold no scenario (.adl or .md)")
	}
	sel := tierSelection{available: len(keys)}
	for _, k := range keys {
		ref, _ := scenarioTestRef(k)
		in, err := inTier(tier, ref, data[k])
		if err != nil {
			return tierSelection{}, err
		}
		if in {
			sel.scripts = append(sel.scripts, kubemootv1alpha1.SuiteScript{TestRef: ref, TestContent: data[k]})
		}
	}
	if len(sel.scripts) > kubemootv1alpha1.MaxStatusScenarios {
		return tierSelection{}, fmt.Errorf("tier %q selects %d scenarios; a suite lists at most %d",
			tier, len(sel.scripts), kubemootv1alpha1.MaxStatusScenarios)
	}
	if len(sel.scripts) == 0 {
		return tierSelection{}, fmt.Errorf("no scenario carries tier %q (add DEFINE CONST %s AS %q to the scenarios it should run)",
			tier, kubemootv1alpha1.TierScenariosConstant, string(tier))
	}
	return sel, nil
}

// inTier reports whether a scenario belongs to tier, and rejects a TIERS label this
// operator does not know. Full ignores labels.
func inTier(tier kubemootv1alpha1.FitnessTier, ref, content string) (bool, error) {
	if tier == kubemootv1alpha1.FitnessTierFull {
		return true, nil
	}
	member := false
	for _, name := range scenarioTiers(content) {
		if !isLabelledTier(name) {
			return false, fmt.Errorf("scenario %q names unknown tier %q in %s (known: smoke, standard)",
				ref, name, kubemootv1alpha1.TierScenariosConstant)
		}
		member = member || name == string(tier)
	}
	return member, nil
}

// tierStatus builds status.tier from a selection.
func tierStatus(tier kubemootv1alpha1.FitnessTier, source string, sel tierSelection) *kubemootv1alpha1.SuiteTierStatus {
	names := make([]string, 0, len(sel.scripts))
	for _, s := range sel.scripts {
		if len(names) == kubemootv1alpha1.MaxStatusScenarios {
			break
		}
		names = append(names, statusName(s.TestRef))
	}
	return &kubemootv1alpha1.SuiteTierStatus{
		Name:       tier,
		Source:     source,
		Scenarios:  names,
		Available:  int32(sel.available),
		ResolvedAt: metav1.Now(),
	}
}

// resolveSuiteTier turns spec.tier into spec.scripts when the suite starts. It writes
// status.tier first and the scripts second, so a failure between the two leaves a
// suite the next reconcile resolves again, never one that looks resolved without
// its scripts. stop is true when the suite ended in Error or a write failed.
func (r *CrewFitnessSuiteReconciler) resolveSuiteTier(ctx context.Context, suite *kubemootv1alpha1.CrewFitnessSuite) (res ctrl.Result, stop bool, err error) {
	if suite.Spec.Tier == "" || (suite.Status.Tier != nil && len(suite.Spec.Scripts) > 0) {
		return ctrl.Result{}, false, nil // resolved on an earlier reconcile
	}
	if len(suite.Spec.Scripts) > 0 {
		res, err = r.setSuiteError(ctx, suite,
			"spec.tier selects the scripts from the crew's scenarios: remove spec.scripts or spec.tier")
		return res, true, err
	}
	source := fitnessConfigMapName(suite.Spec.CrewRef)
	var cm corev1.ConfigMap
	if gerr := r.configMapReader().Get(ctx, client.ObjectKey{Namespace: suite.Namespace, Name: source}, &cm); gerr != nil {
		if client.IgnoreNotFound(gerr) != nil {
			return ctrl.Result{}, true, gerr
		}
		res, err = r.setSuiteError(ctx, suite, fmt.Sprintf(
			"spec.tier %s needs the crew's scenarios: ConfigMap %s not found in namespace %s", suite.Spec.Tier, source, suite.Namespace))
		return res, true, err
	}
	sel, serr := selectTier(suite.Spec.Tier, cm.Data)
	if serr != nil {
		res, err = r.setSuiteError(ctx, suite, fmt.Sprintf("spec.tier %s from ConfigMap %s: %v", suite.Spec.Tier, source, serr))
		return res, true, err
	}
	suite.Status.Tier = tierStatus(suite.Spec.Tier, source, sel)
	if err = r.Status().Update(ctx, suite); err != nil {
		return ctrl.Result{}, true, err
	}
	suite.Spec.Scripts = sel.scripts
	if err = r.Update(ctx, suite); err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{}, false, nil
}

// configMapReader reads the crew's scenarios straight from the API server, so a
// scenario the crew chart just changed is never read stale and the operator keeps no
// informer for every ConfigMap of the cluster. Nil APIReader falls back to Client.
func (r *CrewFitnessSuiteReconciler) configMapReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}
