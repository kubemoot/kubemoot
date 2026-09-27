package ai.kubemoot.indexer.service;

import ai.kubemoot.indexer.source.Downloads;
import ai.kubemoot.indexer.config.IndexerConfig;
import org.eclipse.jgit.api.Git;
import org.eclipse.jgit.api.LsRemoteCommand;
import org.eclipse.jgit.lib.Ref;
import org.eclipse.jgit.transport.UsernamePasswordCredentialsProvider;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpMethod;
import org.springframework.stereotype.Service;
import org.springframework.web.reactive.function.client.WebClient;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.util.Collection;
import java.util.HexFormat;

/**
 * Service for calculating source checksums using metadata only (no full data pull).
 *
 * <p>Checksum calculation by source type:
 * <ul>
 *   <li>Git: Uses git ls-remote to get commit SHA for the branch (no clone required)</li>
 *   <li>S3: Uses AWS SDK listObjectsV2 to hash key+ETag list (no download)</li>
 *   <li>URL: Uses HTTP HEAD requests to hash ETag/Last-Modified headers (no body)</li>
 *   <li>MCP-Registry: Uses registry API to hash server names/versions</li>
 *   <li>Document: Uses HTTP HEAD on each URL to hash ETag/Last-Modified/Content-Length</li>
 * </ul>
 */
@Service
public class ChecksumService {

    private static final Logger logger = LoggerFactory.getLogger(ChecksumService.class);

    private final IndexerConfig config;
    private final WebClient webClient;

    public ChecksumService(IndexerConfig config, WebClient.Builder webClientBuilder) {
        this.config = config;
        this.webClient = Downloads.client(webClientBuilder);
    }

    /**
     * Calculate a checksum for the current source using metadata only.
     *
     * @return checksum string in format "sha256:hex..." or null if calculation fails
     */
    public String calculateChecksum() {
        try {
            String rawChecksum = switch (config.sourceType().toLowerCase()) {
                case "git" -> calculateGitChecksum();
                case "s3" -> calculateS3Checksum();
                case "url" -> calculateUrlChecksum();
                case "mcp-registry" -> calculateMcpRegistryChecksum();
                case "document" -> calculateDocumentChecksum();
                case "nats-kv" -> System.getenv("KUBEMOOT_NATS_KV_CONTENT_HASH");
                default -> {
                    logger.warn("Unknown source type for checksum: {}", config.sourceType());
                    yield null;
                }
            };

            if (rawChecksum == null) {
                return null;
            }

            // Hash the raw checksum to normalize format
            return "sha256:" + sha256(rawChecksum);

        } catch (Exception e) {
            logger.warn("Failed to calculate checksum for source type '{}': {}",
                config.sourceType(), e.getMessage());
            return null;
        }
    }

    /**
     * Calculate checksum for Git source using ls-remote (no clone required).
     * Returns the commit SHA of the target branch.
     */
    private String calculateGitChecksum() {
        if (config.gitUrl() == null || config.gitUrl().isBlank()) {
            logger.warn("Git URL not configured");
            return null;
        }

        try {
            logger.info("Calculating Git checksum for {} (branch: {})",
                config.gitUrl(), config.gitBranch());

            LsRemoteCommand lsRemote = Git.lsRemoteRepository()
                .setRemote(config.gitUrl())
                .setHeads(true)
                .setTags(false);

            // Add credentials if configured
            if (config.gitToken() != null && !config.gitToken().isBlank()) {
                lsRemote.setCredentialsProvider(
                    new UsernamePasswordCredentialsProvider("token", config.gitToken()));
            }

            Collection<Ref> refs = lsRemote.call();

            String targetRef = "refs/heads/" + config.gitBranch();
            for (Ref ref : refs) {
                if (ref.getName().equals(targetRef)) {
                    String sha = ref.getObjectId().getName();
                    logger.info("Git checksum (commit SHA): {}", sha);
                    return sha;
                }
            }

            logger.warn("Branch '{}' not found in remote refs", config.gitBranch());
            return null;

        } catch (Exception e) {
            logger.error("Failed to calculate Git checksum", e);
            return null;
        }
    }

    /**
     * Calculate checksum for S3 source using list objects metadata.
     * Returns hash of (key+ETag) list.
     */
    private String calculateS3Checksum() {
        // S3 checksum calculation would require AWS SDK
        // For now, return null to trigger full indexing
        logger.info("S3 checksum calculation not yet implemented - will always reindex");
        return null;
    }

    /**
     * Calculate checksum for URL source using HTTP HEAD request.
     * Returns hash of ETag and/or Last-Modified headers.
     */
    private String calculateUrlChecksum() {
        if (config.url() == null || config.url().isBlank()) {
            logger.warn("URL not configured");
            return null;
        }

        try {
            logger.info("Calculating URL checksum for {}", config.url());

            // Use HEAD request to get metadata without downloading body
            var response = webClient.method(HttpMethod.HEAD)
                .uri(config.url())
                .exchangeToMono(clientResponse -> {
                    var headers = clientResponse.headers().asHttpHeaders();
                    String etag = headers.getETag();
                    String lastModified = String.valueOf(headers.getLastModified());
                    return reactor.core.publisher.Mono.just(etag + "|" + lastModified);
                })
                .block();

            if (response != null && !response.equals("null|-1")) {
                logger.info("URL checksum (headers): {}", response);
                return response;
            }

            logger.warn("No usable headers (ETag/Last-Modified) from URL");
            return null;

        } catch (Exception e) {
            logger.error("Failed to calculate URL checksum", e);
            return null;
        }
    }

    /**
     * Calculate checksum for MCP Registry source.
     * Returns hash of server names and versions from the registry.
     */
    private String calculateMcpRegistryChecksum() {
        if (config.mcpRegistryUrl() == null || config.mcpRegistryUrl().isBlank()) {
            logger.warn("MCP Registry URL not configured");
            return null;
        }

        try {
            logger.info("Calculating MCP Registry checksum for {}", config.mcpRegistryUrl());

            // Fetch just the server list (lightweight)
            String response = webClient.get()
                .uri(config.mcpRegistryUrl() + "/servers")
                .retrieve()
                .bodyToMono(String.class)
                .block();

            if (response != null) {
                // Hash the entire response as the checksum
                logger.info("MCP Registry checksum calculated ({} bytes)", response.length());
                return response;
            }

            return null;

        } catch (Exception e) {
            logger.error("Failed to calculate MCP Registry checksum", e);
            return null;
        }
    }

    /**
     * Calculate checksum for Document source using HTTP HEAD requests.
     * Returns hash of ETag + Last-Modified + Content-Length for each URL.
     */
    private String calculateDocumentChecksum() {
        var urls = config.documentUrls();
        if (urls == null || urls.isEmpty()) {
            logger.warn("Document URLs not configured");
            return null;
        }

        try {
            logger.info("Calculating Document checksum for {} URLs", urls.size());

            StringBuilder sb = new StringBuilder();
            for (String url : urls) {
                sb.append(url).append("=").append(headDocumentMetadata(url)).append("\n");
            }

            String result = sb.toString();
            logger.info("Document checksum calculated ({} bytes)", result.length());
            return result;

        } catch (Exception e) {
            logger.error("Failed to calculate Document checksum", e);
            return null;
        }
    }

    /**
     * Issue a single HTTP HEAD request and return a metadata fingerprint for one URL.
     * Returns the "etag|lastModified|contentLength" value, or "error" if the request fails.
     */
    private String headDocumentMetadata(String url) {
        try {
            var response = webClient.method(HttpMethod.HEAD)
                .uri(url)
                .exchangeToMono(clientResponse -> {
                    var headers = clientResponse.headers().asHttpHeaders();
                    String etag = headers.getETag();
                    long lastModified = headers.getLastModified();
                    long contentLength = headers.getContentLength();
                    return reactor.core.publisher.Mono.just(
                        etag + "|" + lastModified + "|" + contentLength);
                })
                .block();
            return String.valueOf(response);
        } catch (Exception e) {
            logger.warn("Failed HEAD request for {}: {}", url, e.getMessage());
            return "error";
        }
    }

    /**
     * Calculate SHA-256 hash of a string.
     */
    private String sha256(String input) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            byte[] hash = digest.digest(input.getBytes(StandardCharsets.UTF_8));
            return HexFormat.of().formatHex(hash);
        } catch (NoSuchAlgorithmException e) {
            throw new IllegalStateException("SHA-256 not available", e);
        }
    }

    /**
     * Check if force reindex is enabled (bypasses checksum comparison).
     */
    public boolean isForceReindex() {
        return "true".equalsIgnoreCase(config.forceReindex());
    }

    /**
     * Get the last known checksum from the controller.
     */
    public String getLastChecksum() {
        return config.lastChecksum();
    }

    /**
     * Check if the source has changed by comparing checksums.
     *
     * @return true if source has changed or checksum cannot be determined
     */
    public boolean hasSourceChanged() {
        if (isForceReindex()) {
            logger.info("Force reindex enabled - skipping checksum comparison");
            return true;
        }

        String lastChecksum = getLastChecksum();
        if (lastChecksum == null || lastChecksum.isBlank()) {
            logger.info("No previous checksum - source considered changed");
            return true;
        }

        String currentChecksum = calculateChecksum();
        if (currentChecksum == null) {
            logger.info("Could not calculate current checksum - source considered changed");
            return true;
        }

        boolean changed = !currentChecksum.equals(lastChecksum);
        if (changed) {
            logger.info("Source changed: {} -> {}", lastChecksum, currentChecksum);
        } else {
            logger.info("Source unchanged: {}", currentChecksum);
        }

        return changed;
    }
}
