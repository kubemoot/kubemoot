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
	serverProp := stringProp("The ModelServer's name, as list_servers shows it.")
	return []map[string]any{
		spec(ToolListServers,
			"List the cluster's model servers: readiness, engine and version, memory "+
				"(total and the budget a model must fit), whether it is an accelerator, how many "+
				"requests run together, the calibration status, and any reset a person requested.",
			object(nil, map[string]any{})),
		spec(ToolServerState,
			"Show one server now: the models loaded and how much of each is on the accelerator, "+
				"how many other calls hold it, the models those calls use, the models downloaded with "+
				"their sizes, and the models Model resources bind to it.",
			object([]string{fieldServer}, map[string]any{fieldServer: serverProp})),
		spec(ToolProbeModel,
			"Send one calibration probe to a model on a server and return the engine's timings. "+
				"load: load the model and report the load time, its memory, and whether it spills to the CPU. "+
				"short: a short request, for generation speed. long: a long prompt, for prompt speed. "+
				"parallel: as many short requests as the server runs together, for total speed. "+
				"The probe claims tickets like any call. It sends nothing and returns busy: true while other "+
				"calls hold the server, and refuses a model larger than the server's memory budget.",
			object([]string{fieldServer, fieldModel, fieldProbe}, map[string]any{
				fieldServer: serverProp,
				fieldModel:  stringProp("The model name on the server, such as qwen3:14b."),
				"probe":     enumProp("Which probe to send.", "load", "short", "long", "parallel"),
			})),
		spec(ToolCalibrateServer,
			"Calibrate a server: in the background, measure each model with the fixed recipe (load, two "+
				"short requests, a long prompt, and a parallel batch when the server runs requests together) "+
				"and record the results on the server. Returns at once with the models it will measure. "+
				"Without models it measures the downloaded models that Model resources bind to the server, "+
				"else the three smallest that fit. It pauses as Waiting when crew calls take the server. "+
				"Follow progress with list_servers.",
			object([]string{fieldServer}, map[string]any{
				fieldServer: serverProp,
				fieldModels: map[string]any{
					fieldType:        "array",
					fieldDescription: "Optional: the models to measure, by name on the server.",
					"items":          map[string]any{fieldType: typeString},
				},
			})),
		spec(ToolObservedPerformance,
			"Compare crew calls on a server since its calibration with what the calibration measured: per "+
				"model, the median generation speed of warm calls that ran alone and of calls that ran alongside "+
				"others, the medians of the earlier and later half of the calls alone, sample counts, failures, the "+
				"time span, the most calls seen at once, callsNow (calls by other holders on the server now), and "+
				"which models drifted by the operator's rule (stated in driftRule).",
			object([]string{fieldServer}, map[string]any{fieldServer: serverProp})),
		spec(ToolRecordCalibration,
			"Record a calibration on the server's status. Phase Calibrating marks the start (and, for a "+
				"reset of scope all, forgets the server's performance history). Calibrated, Waiting (paused "+
				"for crew calls), and Failed record the per-model results and unload the models the probes loaded.",
			object([]string{fieldServer, fieldPhase}, map[string]any{
				fieldServer: serverProp,
				fieldPhase:  enumProp("The calibration phase.", PhaseCalibrating, PhaseCalibrated, PhaseWaiting, PhaseFailed),
				"message":   stringProp("A short explanation shown on the server."),
				fieldModels: map[string]any{
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
			fieldDescription: "False when it does not fit the server's memory or spills to the CPU."},
		"reason": stringProp("Why it does not fit, was skipped, or could not be measured."),
		"status": enumProp("Measured (the default), Skipped (does not apply, such as an embedding model), or Failed.",
			"Measured", "Skipped", "Failed"),
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
	return map[string]any{fieldType: typeString, fieldDescription: description}
}

func enumProp(description string, values ...string) map[string]any {
	return map[string]any{fieldType: typeString, fieldDescription: description, "enum": values}
}
