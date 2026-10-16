/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

// Specifications lists the tools with their JSON input schemas, for tools/list.
func (s *Set) Specifications() []map[string]any {
	providerProp := stringProp("The ModelProvider's name, as list_providers shows it.")
	return []map[string]any{
		spec(ToolListProviders,
			"List the cluster's model providers: readiness, engine and version, memory "+
				"(total and the budget a model must fit), whether it is an accelerator, how many "+
				"requests run together, the calibration status, and any reset a person requested.",
			object(nil, map[string]any{})),
		spec(ToolProviderState,
			"Show one provider now: the models loaded and how much of each is on the accelerator, "+
				"how many other calls hold it, the models those calls use, the models downloaded with "+
				"their sizes, and the models Model resources bind to it.",
			object([]string{fieldProvider}, map[string]any{fieldProvider: providerProp})),
		spec(ToolProbeModel,
			"Send one calibration probe to a model on a provider and return the engine's timings. "+
				"load: load the model and report the load time, its memory, and whether it spills to the CPU. "+
				"short: a short request, for generation speed. long: a long prompt, for prompt speed. "+
				"parallel: as many short requests as the provider runs together, for total speed. "+
				"The probe claims tickets like any call. It sends nothing and returns busy: true while other "+
				"calls hold the provider, and refuses a model larger than the provider's memory budget.",
			object([]string{fieldProvider, fieldModel, fieldProbe}, map[string]any{
				fieldProvider: providerProp,
				fieldModel:    stringProp("The model name on the provider, such as qwen3:14b."),
				"probe":       enumProp("Which probe to send.", "load", "short", "long", "parallel"),
			})),
		spec(ToolRecordCalibration,
			"Record a calibration on the provider's status. Phase Calibrating marks the start (and, for a "+
				"reset of scope all, forgets the provider's performance history). Calibrated, Waiting (paused "+
				"for crew calls), and Failed record the per-model results and unload the models the probes loaded.",
			object([]string{fieldProvider, fieldPhase}, map[string]any{
				fieldProvider: providerProp,
				fieldPhase:    enumProp("The calibration phase.", PhaseCalibrating, PhaseCalibrated, PhaseWaiting, PhaseFailed),
				"message":     stringProp("A short explanation shown on the provider."),
				"models": map[string]any{
					fieldType:        "array",
					fieldDescription: "Per-model results; each replaces an earlier result for the same model.",
					"items":          modelResultSchema(),
				},
			})),
	}
}

func modelResultSchema() map[string]any {
	integer := func(d string) map[string]any { return map[string]any{fieldType: "integer", fieldDescription: d} }
	return object([]string{fieldModel, "fits"}, map[string]any{
		fieldModel: stringProp("The model name."),
		"fits": map[string]any{fieldType: "boolean",
			fieldDescription: "False when it does not fit the provider's memory or spills to the CPU."},
		"reason":                          stringProp("Why it does not fit or could not be measured."),
		"loadMs":                          integer("Load time in milliseconds."),
		"residentMiB":                     integer("Memory it took once loaded."),
		"contextLength":                   integer("Context the engine gives it, in tokens."),
		"promptTokensPerSecond":           integer("Prompt processing speed."),
		"generateTokensPerSecond":         integer("Generation speed of one request."),
		"parallelRequests":                integer("Requests sent together for the parallel measure."),
		"parallelGenerateTokensPerSecond": integer("Total generation speed with them together."),
	})
}

func spec(name, description string, inputSchema map[string]any) map[string]any {
	return map[string]any{"name": name, fieldDescription: description, "inputSchema": inputSchema}
}

func object(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{fieldType: "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProp(description string) map[string]any {
	return map[string]any{fieldType: "string", fieldDescription: description}
}

func enumProp(description string, values ...string) map[string]any {
	return map[string]any{fieldType: "string", fieldDescription: description, "enum": values}
}
