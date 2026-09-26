package ai.kubemoot.indexer.source;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.document.Document;
import org.springframework.stereotype.Component;
import org.springframework.web.reactive.function.client.WebClient;

import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Loads documents by converting them via docling-serve sidecar.
 * Supports PDF, DOCX, PPTX, HTML, and images.
 * The docling-serve sidecar runs as a native sidecar in the indexer Job pod.
 */
@Component
public class DocumentSourceLoader implements SourceLoader {

    private static final Logger logger = LoggerFactory.getLogger(DocumentSourceLoader.class);

    private static final String SOURCE_TYPE_DOCUMENT = "document";

    private final IndexerConfig config;
    private final WebClient webClient;

    public DocumentSourceLoader(IndexerConfig config, WebClient.Builder webClientBuilder) {
        this.config = config;
        this.webClient = webClientBuilder.build();
    }

    @Override
    public boolean supports(String sourceType) {
        return SOURCE_TYPE_DOCUMENT.equalsIgnoreCase(sourceType);
    }

    @Override
    public List<Document> load() {
        List<String> urls = config.documentUrls();
        if (urls == null || urls.isEmpty()) {
            throw new IllegalArgumentException("KUBEMOOT_DOCUMENT_URLS is required for document source");
        }

        String endpoint = config.doclingEndpoint();
        logger.info("Using docling-serve at {}", endpoint);

        // Wait for docling-serve to be ready
        waitForDoclingServe(endpoint);

        List<Document> documents = new ArrayList<>();
        for (String url : urls) {
            try {
                logger.info("Converting document: {}", url);
                String markdown = convertDocument(endpoint, url);
                if (markdown != null && !markdown.isBlank()) {
                    Map<String, Object> metadata = new HashMap<>();
                    metadata.put("source", SOURCE_TYPE_DOCUMENT);
                    metadata.put("file_path", url);
                    documents.add(new Document(markdown, metadata));
                    logger.info("Converted document ({} chars): {}", markdown.length(), url);
                } else {
                    logger.warn("Empty conversion result for: {}", url);
                }
            } catch (Exception e) {
                logger.error("Failed to convert document: {}", url, e);
            }
        }

        logger.info("Loaded {} documents from {} URLs", documents.size(), urls.size());
        return documents;
    }

    /**
     * Convert a document URL via docling-serve REST API.
     * POST /v1/convert/source with {"source": url}
     */
    private String convertDocument(String endpoint, String url) {
        Map<String, Object> body = Map.of("source", url);

        @SuppressWarnings("unchecked")
        Map<String, Object> response = webClient.post()
            .uri(endpoint + "/v1/convert/source")
            .bodyValue(body)
            .retrieve()
            .bodyToMono(Map.class)
            .block(Duration.ofMinutes(5));

        if (response == null) {
            return null;
        }

        // Extract markdown from the response. The response carries a nested document
        // object whose markdown lives under the md_content key (docling-serve v2) or the
        // export_to_markdown key (docling-serve v1).
        Object document = response.get(SOURCE_TYPE_DOCUMENT);
        if (document instanceof Map) {
            @SuppressWarnings("unchecked")
            Map<String, Object> docMap = (Map<String, Object>) document;
            // Try md_content first (docling-serve v2)
            Object mdContent = docMap.get("md_content");
            if (mdContent instanceof String) {
                return (String) mdContent;
            }
            // Fall back to export_to_markdown (docling-serve v1)
            Object exportMd = docMap.get("export_to_markdown");
            if (exportMd instanceof String) {
                return (String) exportMd;
            }
        }

        // If response has a direct "markdown" key
        Object markdown = response.get("markdown");
        if (markdown instanceof String) {
            return (String) markdown;
        }

        logger.warn("Could not extract markdown from docling-serve response: {}", response.keySet());
        return null;
    }

    /**
     * Wait for docling-serve to be ready by polling /health endpoint.
     */
    private void waitForDoclingServe(String endpoint) {
        int maxRetries = 60;
        for (int i = 0; i < maxRetries; i++) {
            try {
                String health = webClient.get()
                    .uri(endpoint + "/health")
                    .retrieve()
                    .bodyToMono(String.class)
                    .block(Duration.ofSeconds(5));
                if (health != null) {
                    logger.info("docling-serve is ready");
                    return;
                }
            } catch (Exception e) {
                if (i % 10 == 0) {
                    logger.info("Waiting for docling-serve... (attempt {}/{})", i + 1, maxRetries);
                }
            }
            try {
                Thread.sleep(3000);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
                throw new IllegalStateException("Interrupted while waiting for docling-serve", e);
            }
        }
        throw new IllegalStateException("docling-serve did not become ready within timeout");
    }
}
