// Reads a ModelServer's accelerator capacity through the current field names, falling back to
// the deprecated old names (vramUsedMiB, vramTotalMiB, gpuModel, sizeVram) that an operator
// older than this release writes. Drop the fallbacks when the operator stops writing them.

export interface CapacityLike {
	acceleratorMemoryUsedMiB?: number;
	acceleratorMemoryTotalMiB?: number;
	acceleratorModel?: string;
	vramUsedMiB?: number;
	vramTotalMiB?: number;
	gpuModel?: string;
}

interface LoadedModelLike {
	acceleratorMemory?: number;
	sizeVram?: number;
}

/**
 * The current field's value, or the deprecated old name's when the current one is unset
 * (undefined or an empty string). The one rule every accelerator fallback shares; a number
 * of 0 is a value, not unset.
 */
export function preferred<T>(current: T | undefined, deprecated: T | undefined): T | undefined {
	return current === undefined || current === '' ? deprecated : current;
}

/** Accelerator memory in MiB the loaded models hold; undefined when the status gives none. */
export function acceleratorMemoryUsedMiB(capacity?: CapacityLike): number | undefined {
	return preferred(capacity?.acceleratorMemoryUsedMiB, capacity?.vramUsedMiB);
}

/** Accelerator memory in MiB the server can use; undefined when the status gives none. */
export function acceleratorMemoryTotalMiB(capacity?: CapacityLike): number | undefined {
	return preferred(capacity?.acceleratorMemoryTotalMiB, capacity?.vramTotalMiB);
}

/** The accelerator's name as the operator discovered it; undefined when it has none. */
export function acceleratorModel(capacity?: CapacityLike): string | undefined {
	return preferred(capacity?.acceleratorModel, capacity?.gpuModel) || undefined;
}

/** Accelerator memory in bytes one loaded model holds. */
export function loadedAcceleratorMemory(model?: LoadedModelLike): number | undefined {
	return preferred(model?.acceleratorMemory, model?.sizeVram);
}

/** The accelerator a fitness or stream event names: `accelerator`, else the deprecated `gpu`. */
export function acceleratorOfEvent(event: { accelerator?: string; gpu?: string }): string | undefined {
	return preferred(event.accelerator, event.gpu) || undefined;
}
