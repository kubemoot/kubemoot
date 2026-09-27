package ai.kubemoot.indexer.source;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.document.Document;
import org.springframework.stereotype.Component;
import org.springframework.web.reactive.function.client.WebClient;

import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * Loads documents from a URL.
 */
@Component
public class UrlSourceLoader implements SourceLoader {

    private static final Logger logger = LoggerFactory.getLogger(UrlSourceLoader.class);

    private final IndexerConfig config;
    private final WebClient webClient;

    public UrlSourceLoader(IndexerConfig config, WebClient.Builder webClientBuilder) {
        this.config = config;
        this.webClient = Downloads.client(webClientBuilder);
    }

    @Override
    public boolean supports(String sourceType) {
        return "url".equalsIgnoreCase(sourceType);
    }

    @Override
    public List<Document> load() {
        if (config.url() == null || config.url().isBlank()) {
            throw new IllegalArgumentException("KUBEMOOT_URL is required for url source");
        }

        logger.info("Fetching from URL: {}", config.url());

        String content = webClient.get()
            .uri(config.url())
            .retrieve()
            .bodyToMono(String.class)
            .block();

        Map<String, Object> metadata = new HashMap<>();
        metadata.put("source", "url");
        metadata.put("file_path", config.url());

        logger.info("Loaded document from URL ({} chars)", content != null ? content.length() : 0);

        return List.of(new Document(content, metadata));
    }
}
