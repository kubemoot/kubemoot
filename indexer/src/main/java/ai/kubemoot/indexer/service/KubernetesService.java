package ai.kubemoot.indexer.service;

import io.fabric8.kubernetes.api.model.batch.v1.Job;
import io.fabric8.kubernetes.client.KubernetesClient;
import io.fabric8.kubernetes.client.KubernetesClientBuilder;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;

import jakarta.annotation.PreDestroy;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Service for interacting with Kubernetes API.
 *
 * <p>Used to annotate the current Job with indexing results (checksum, status, counts)
 * so the controller can read them when the job completes.
 */
@Service
public class KubernetesService {

    private static final Logger logger = LoggerFactory.getLogger(KubernetesService.class);

    private static final String ANNOTATION_STATUS = "kubemoot.ai/status";
    private static final String ANNOTATION_CHECKSUM = "kubemoot.ai/checksum";
    private static final String ANNOTATION_DOCUMENT_COUNT = "kubemoot.ai/document-count";
    private static final String ANNOTATION_CHUNK_COUNT = "kubemoot.ai/chunk-count";
    private static final String ANNOTATION_TOPICS = "kubemoot.ai/topics";

    private final KubernetesClient client;
    private final String namespace;
    private final String jobName;

    public KubernetesService() {
        // Auto-detect configuration from in-cluster or kubeconfig
        this.client = new KubernetesClientBuilder().build();

        // Get current namespace from environment or service account
        this.namespace = getNamespace();
        this.jobName = getJobName();

        logger.info("Kubernetes service initialized: namespace={}, job={}",
            namespace, jobName != null ? jobName : "unknown");
    }

    @PreDestroy
    public void close() {
        if (client != null) {
            client.close();
        }
    }

    /**
     * Record that indexing was skipped because source is unchanged.
     */
    public void recordUnchanged() {
        annotateJob(Map.of(ANNOTATION_STATUS, "unchanged"));
    }

    /**
     * Record successful indexing with checksum and counts.
     */
    public void recordIndexed(String checksum, int documentCount, int chunkCount) {
        Map<String, String> annotations = new HashMap<>();
        annotations.put(ANNOTATION_STATUS, "indexed");
        if (checksum != null) {
            annotations.put(ANNOTATION_CHECKSUM, checksum);
        }
        annotations.put(ANNOTATION_DOCUMENT_COUNT, String.valueOf(documentCount));
        annotations.put(ANNOTATION_CHUNK_COUNT, String.valueOf(chunkCount));

        annotateJob(annotations);
    }

    /**
     * Record extracted topics as a comma-separated annotation on the Job.
     * The RAGSource controller reads these to populate status.topics for auto-discovery.
     */
    public void recordTopics(List<String> topics) {
        if (topics == null || topics.isEmpty()) {
            return;
        }
        annotateJob(Map.of(ANNOTATION_TOPICS, String.join(",", topics)));
    }

    /**
     * Annotate the current Job with the given annotations.
     */
    private void annotateJob(Map<String, String> annotations) {
        if (jobName == null || namespace == null) {
            logger.warn("Cannot annotate Job - job name or namespace not available");
            // Fall back to logging for non-K8s environments (local testing)
            logger.info("Would annotate Job with: {}", annotations);
            return;
        }

        try {
            Job job = client.batch().v1().jobs()
                .inNamespace(namespace)
                .withName(jobName)
                .get();

            if (job == null) {
                logger.warn("Job not found: {}/{}", namespace, jobName);
                return;
            }

            // Merge new annotations with existing ones
            final Map<String, String> mergedAnnotations = new HashMap<>();
            Map<String, String> existingAnnotations = job.getMetadata().getAnnotations();
            if (existingAnnotations != null) {
                mergedAnnotations.putAll(existingAnnotations);
            }
            mergedAnnotations.putAll(annotations);

            client.batch().v1().jobs()
                .inNamespace(namespace)
                .withName(jobName)
                .edit(j -> {
                    j.getMetadata().setAnnotations(mergedAnnotations);
                    return j;
                });

            logger.info("Annotated Job {}/{} with: {}", namespace, jobName, annotations);

        } catch (Exception e) {
            logger.error("Failed to annotate Job {}/{}", namespace, jobName, e);
        }
    }

    /**
     * Get the current namespace from Kubernetes service account or environment.
     */
    private String getNamespace() {
        // First check environment variable (set by Kubernetes downward API)
        String ns = System.getenv("POD_NAMESPACE");
        if (ns != null && !ns.isBlank()) {
            return ns;
        }

        // Try to read from service account
        try {
            java.nio.file.Path nsPath = java.nio.file.Path.of(
                "/var/run/secrets/kubernetes.io/serviceaccount/namespace");
            if (java.nio.file.Files.exists(nsPath)) {
                return java.nio.file.Files.readString(nsPath).trim();
            }
        } catch (Exception e) {
            logger.debug("Could not read namespace from service account: {}", e.getMessage());
        }

        // Fall back to client's namespace
        return client.getNamespace();
    }

    /**
     * Get the current Job name from environment.
     */
    private String getJobName() {
        // Job name is typically set via JOB_NAME environment variable
        // or can be extracted from POD_NAME (which follows pattern: jobname-xxxxx)
        String jobNameEnv = System.getenv("JOB_NAME");
        if (jobNameEnv != null && !jobNameEnv.isBlank()) {
            return jobNameEnv;
        }

        // Try to extract from pod name
        String podName = System.getenv("POD_NAME");
        if (podName != null && !podName.isBlank()) {
            // Job pod names follow pattern: jobname-random
            int lastDash = podName.lastIndexOf('-');
            if (lastDash > 0) {
                return podName.substring(0, lastDash);
            }
        }

        return null;
    }

    /**
     * Check if we're running inside Kubernetes.
     */
    public boolean isRunningInKubernetes() {
        return jobName != null && namespace != null;
    }
}
