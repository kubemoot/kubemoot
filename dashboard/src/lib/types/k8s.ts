// Kubernetes core types

export interface K8sNode {
	apiVersion: string;
	kind: 'Node';
	metadata: {
		name: string;
		uid?: string;
		labels?: Record<string, string>;
		annotations?: Record<string, string>;
		creationTimestamp?: string;
	};
	spec: {
		podCIDR?: string;
		providerID?: string;
		taints?: Taint[];
	};
	status: {
		conditions?: NodeCondition[];
		addresses?: NodeAddress[];
		capacity?: Record<string, string>;
		allocatable?: Record<string, string>;
		nodeInfo?: NodeSystemInfo;
	};
}

export interface Taint {
	key: string;
	value?: string;
	effect: 'NoSchedule' | 'PreferNoSchedule' | 'NoExecute';
}

export interface NodeCondition {
	type: string;
	status: 'True' | 'False' | 'Unknown';
	reason?: string;
	message?: string;
	lastHeartbeatTime?: string;
	lastTransitionTime?: string;
}

export interface NodeAddress {
	type: 'Hostname' | 'InternalIP' | 'ExternalIP' | 'InternalDNS' | 'ExternalDNS';
	address: string;
}

export interface NodeSystemInfo {
	machineID?: string;
	systemUUID?: string;
	bootID?: string;
	kernelVersion?: string;
	osImage?: string;
	containerRuntimeVersion?: string;
	kubeletVersion?: string;
	kubeProxyVersion?: string;
	operatingSystem?: string;
	architecture?: string;
}

export interface K8sNamespace {
	apiVersion: string;
	kind: 'Namespace';
	metadata: {
		name: string;
		uid?: string;
		labels?: Record<string, string>;
		creationTimestamp?: string;
	};
	status?: {
		phase?: 'Active' | 'Terminating';
	};
}

// GPU-related types
export interface GPUInfo {
	present: boolean;
	type?: string;
	count?: number;
	memory?: string;
	allocatable?: string;
	capacity?: string;
}

export interface NodeWithGPU extends K8sNode {
	gpu?: GPUInfo;
}

// API response types
export interface HealthResponse {
	status: 'healthy' | 'unhealthy';
	timestamp: string;
	kubernetes?: {
		connected: boolean;
		version?: string;
	};
}

export interface VersionResponse {
	version: string;
	buildTime?: string;
	gitCommit?: string;
}

export interface NamespacesResponse {
	namespaces: string[];
}

export interface NodesResponse {
	nodes: NodeWithGPU[];
}

export interface SSEEvent {
	type: 'update' | 'delete' | 'error';
	resource: string;
	namespace?: string;
	name?: string;
	data?: unknown;
	timestamp: string;
}
