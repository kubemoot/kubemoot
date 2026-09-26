package ai.kubemoot.indexer.source;

import org.springframework.ai.document.Document;

import java.util.List;

/**
 * Interface for loading documents from various sources.
 */
public interface SourceLoader {

    /**
     * Check if this loader supports the given source type.
     */
    boolean supports(String sourceType);

    /**
     * Load documents from the source.
     */
    List<Document> load();
}
