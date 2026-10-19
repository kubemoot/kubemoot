/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// catalog.go builds the cluster's model catalog. A ModelProvider publishes its
// part of it in status.models: what its engine reports about the models it has,
// merged with the Model objects in the provider's namespace that name them. The
// merge is pure so every rule of it is a unit test.
package controller

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// conditionTypeCatalogSizeKnown is the ModelProvider condition that says
	// whether every model in the provider's catalog has a size class.
	conditionTypeCatalogSizeKnown = "CatalogSizeKnown"
	reasonAllSizesKnown           = "AllSizesKnown"
	reasonSizeUnknown             = "SizeUnknown"
)

// catalogFacts is what a provider's engine said about one model.
type catalogFacts struct {
	Name    string
	Present bool
	// Source is where ParameterCount came from (an aiv1alpha1.CatalogSource*).
	Source         string
	ParameterCount int64
	ContextLength  int
	Capabilities   []string
	Family         string
	// Embedding is true for a model that only embeds; it never joins the catalog.
	Embedding bool
}

// catalogModelName is the name a Model's model has on the provider's engine: the
// Ollama tag for an ollama provider, the served name for a vLLM one.
func catalogModelName(provider *aiv1alpha1.ModelProvider, m *aiv1alpha1.Model) string {
	if provider.Spec.Type == aiv1alpha1.ProviderTypeVLLM {
		return m.Spec.Model
	}
	return m.OllamaTag()
}

// catalogModels are the Model objects that belong to the provider's catalog:
// those in the provider's namespace that name it and are not being deleted.
func catalogModels(provider *aiv1alpha1.ModelProvider, all []aiv1alpha1.Model) map[string]*aiv1alpha1.Model {
	out := map[string]*aiv1alpha1.Model{}
	for i := range all {
		m := &all[i]
		if m.Namespace != provider.Namespace || m.Spec.ProviderRef != provider.Name || !m.DeletionTimestamp.IsZero() {
			continue
		}
		if name := catalogModelName(provider, m); name != "" && catalogModelBefore(m, out[name]) {
			out[name] = m
		}
	}
	return out
}

// catalogModelBefore reports whether m should speak for its model instead of the
// Model already chosen (nil for none): a disabled Model wins, so a duplicate can
// never re-admit a model its owner disabled; otherwise the older one, then the
// lower name, so the choice does not depend on list order.
func catalogModelBefore(m, chosen *aiv1alpha1.Model) bool {
	switch {
	case chosen == nil:
		return true
	case m.Spec.Disabled != chosen.Spec.Disabled:
		return m.Spec.Disabled
	case !m.CreationTimestamp.Equal(&chosen.CreationTimestamp):
		return m.CreationTimestamp.Before(&chosen.CreationTimestamp)
	}
	return m.Name < chosen.Name
}

// buildCatalog merges what the engine reported (facts) with the provider's Model
// objects into the provider's catalog entries, sorted by name. The Model wins
// wherever it speaks; a disabled Model removes its model; an embedding-only model
// is never admitted. A model that has a Model object but is not reported is
// listed as not present.
func buildCatalog(provider *aiv1alpha1.ModelProvider, facts []catalogFacts, models map[string]*aiv1alpha1.Model) []aiv1alpha1.CatalogModel {
	byName := map[string]catalogFacts{}
	for _, f := range facts {
		byName[f.Name] = f
	}
	for name := range models {
		if _, ok := byName[name]; !ok {
			byName[name] = catalogFacts{Name: name, Source: aiv1alpha1.CatalogSourceNone}
		}
	}
	var out []aiv1alpha1.CatalogModel
	for name, f := range byName {
		m := models[name]
		if f.Embedding || (m != nil && m.Spec.Disabled) {
			continue
		}
		out = append(out, mergeEntry(provider, f, m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// mergeEntry is the catalog entry for one model: the engine's facts overlaid by
// the Model object m, if any.
func mergeEntry(provider *aiv1alpha1.ModelProvider, f catalogFacts, m *aiv1alpha1.Model) aiv1alpha1.CatalogModel {
	e := aiv1alpha1.CatalogModel{
		Name:           f.Name,
		Present:        f.Present,
		Source:         f.Source,
		ParameterCount: f.ParameterCount,
		ContextLength:  f.ContextLength,
		Capabilities:   normalizeAbilities(f.Capabilities),
	}
	if e.Source == "" {
		e.Source = aiv1alpha1.CatalogSourceNone
	}
	if m != nil {
		overlayModel(&e, m)
	}
	e.Labels = entryLabels(provider, f.Family, &e, m)
	e.Size = e.Labels[aiv1alpha1.LabelSize]
	return e
}

// overlayModel applies what a Model object declares onto an entry.
func overlayModel(e *aiv1alpha1.CatalogModel, m *aiv1alpha1.Model) {
	e.ModelRef = m.Namespace + "/" + m.Name
	if n, ok := aiv1alpha1.ParseParameterCount(m.Spec.Parameters); ok {
		e.ParameterCount, e.Source = n, aiv1alpha1.CatalogSourceModel
	}
	if m.Spec.ContextLength > 0 {
		e.ContextLength = m.Spec.ContextLength
	}
	if caps := normalizeAbilities(m.Spec.Capabilities); len(caps) > 0 {
		e.Capabilities = caps
	}
}

// entryLabels are the labels scheduling rules match for an entry: the derived
// ones, then every label on the Model object (so the owner's label wins, the size
// override included).
func entryLabels(provider *aiv1alpha1.ModelProvider, family string, e *aiv1alpha1.CatalogModel, m *aiv1alpha1.Model) map[string]string {
	labels := map[string]string{aiv1alpha1.LabelEngine: string(provider.Spec.Type)}
	if size := aiv1alpha1.SizeClassFor(e.ParameterCount); size != "" {
		labels[aiv1alpha1.LabelSize] = size
	}
	if e.ContextLength > 0 {
		labels[aiv1alpha1.LabelContextTokens] = strconv.Itoa(e.ContextLength)
	}
	if family != "" {
		labels[aiv1alpha1.LabelFamily] = family
	}
	for _, c := range e.Capabilities {
		labels[aiv1alpha1.CapabilityLabelPrefix+c] = "true"
	}
	if m != nil {
		overlayLabels(labels, m.Labels)
	}
	return labels
}

// overlayLabels copies the Model's labels over the derived ones. A kubemoot.ai/size
// label that is not one of the three classes is ignored, so the derived class (or
// none) stands.
func overlayLabels(labels, owner map[string]string) {
	for k, v := range owner {
		if k == aiv1alpha1.LabelSize && !validSize(v) {
			continue
		}
		labels[k] = v
	}
}

// validSize reports whether s is one of the size classes.
func validSize(s string) bool {
	return s == aiv1alpha1.SizeSmall || s == aiv1alpha1.SizeMedium || s == aiv1alpha1.SizeLarge
}

// normalizeAbilities lower-cases, trims, de-duplicates and sorts ability names.
func normalizeAbilities(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// entriesWithoutSize lists the catalog entries that have no size class.
func entriesWithoutSize(entries []aiv1alpha1.CatalogModel) []string {
	var names []string
	for _, e := range entries {
		if e.Size == "" {
			names = append(names, e.Name)
		}
	}
	return names
}

// setCatalogSizeCondition records on the provider whether every catalog entry has
// a size, naming the ones that do not so the owner can declare spec.parameters.
func setCatalogSizeCondition(provider *aiv1alpha1.ModelProvider) {
	unknown := entriesWithoutSize(provider.Status.Models)
	cond := metav1.Condition{
		Type:    conditionTypeCatalogSizeKnown,
		Status:  metav1.ConditionTrue,
		Reason:  reasonAllSizesKnown,
		Message: "Every model in the catalog has a size class",
	}
	if len(unknown) > 0 {
		cond.Status, cond.Reason = metav1.ConditionFalse, reasonSizeUnknown
		cond.Message = fmt.Sprintf("No size for %s: set spec.parameters on a Model for each", strings.Join(unknown, ", "))
	}
	meta.SetStatusCondition(&provider.Status.Conditions, cond)
}
