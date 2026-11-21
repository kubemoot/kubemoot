package controller

import corev1 "k8s.io/api/core/v1"

// envOTLPEndpoint names the OTLP/HTTP collector the agent runtime exports its spans to. The
// sidecar takes the same endpoint from its contract; with none set the runtime exports nothing.
const envOTLPEndpoint = "KUBEMOOT_OTLP_ENDPOINT"

// otlpEnvVars hands the runtime KubemootConfig's default collector, when the config names one.
func (r *AgentReconciler) otlpEnvVars() []corev1.EnvVar {
	if r.ConfigCache == nil {
		return nil
	}
	endpoint := r.ConfigCache.GetOTelCollectorEndpoint()
	if endpoint == "" {
		return nil
	}
	return []corev1.EnvVar{{Name: envOTLPEndpoint, Value: endpoint}}
}
