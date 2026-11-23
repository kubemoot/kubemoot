// What an accelerator is doing right now, in terms that do not name a vendor. An exporter adapter
// (server side) turns a vendor's metrics into this; every page reads only this.

/** One node's accelerators measured together. */
export interface AcceleratorUse {
	/** Devices measured on the node. */
	devices: number;
	/** Share of time the devices were busy, 0 to 1, averaged over the devices; undefined when not reported. */
	busy?: number;
	/** Accelerator memory in use and in all, in MiB, summed over the devices; undefined when not reported. */
	memoryUsedMiB?: number;
	memoryTotalMiB?: number;
}

/** Use per node name. */
export type AcceleratorUseByNode = Record<string, AcceleratorUse>;

/** What /api/accelerator-use answers: not configured, or a reading that says why when it failed. */
export type AcceleratorUseResponse =
	| { configured: false }
	| { configured: true; use: { available: true; data: AcceleratorUseByNode } | { available: false; reason: string } };

/** The measured use, or undefined when no metrics source is set or it could not be read. */
export function measuredUse(response: AcceleratorUseResponse | null | undefined): AcceleratorUseByNode | undefined {
	return response?.configured && response.use.available ? response.use.data : undefined;
}

/** Why a configured metrics source gave no use; undefined when it is not configured or it worked. */
export function useProblem(response: AcceleratorUseResponse | null | undefined): string | undefined {
	return response?.configured && !response.use.available ? response.use.reason : undefined;
}
