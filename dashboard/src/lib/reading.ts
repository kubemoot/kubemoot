/** A reading the person's account could take, or the plain reason it could not. Never a zero in place of a failure. */
export type Reading<T> = { available: true; data: T } | { available: false; reason: string; denied?: boolean };

/** A pod by where it lives. */
export interface PodRef {
	namespace: string;
	name: string;
}

/** Pods on one node: those Running, every pod that still holds a place on it, and the accelerator devices they ask for. */
export interface PodCount {
	running: number;
	total: number;
	accelerators: number;
	/** The pods that hold those accelerator devices. */
	holders: PodRef[];
}

/** A pod as the node page lists it. */
export interface NodePod {
	name: string;
	namespace: string;
	phase: string;
	restarts: number;
	/** Accelerator devices the pod has asked for. */
	accelerators: number;
}
