package ai.kubemoot.indexer.config;

import org.junit.jupiter.api.Test;

import java.nio.file.FileSystems;
import java.nio.file.Path;

import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.Mockito.CALLS_REAL_METHODS;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.withSettings;

class IndexerConfigTest {

    private static boolean included(IndexerConfig config, String path) {
        return config.includePatterns().stream()
            .anyMatch(p -> FileSystems.getDefault().getPathMatcher("glob:" + p).matches(Path.of(path)));
    }

    @Test
    void defaultPatternsIncludeAdlSpecifications() {
        IndexerConfig config = mock(IndexerConfig.class, withSettings().defaultAnswer(CALLS_REAL_METHODS));
        assertTrue(included(config, "order-service.adl"), "an .adl file at the top of a path");
        assertTrue(included(config, "material/order-service.adl"), "an .adl file below it");
        assertTrue(included(config, "material/adl-well-formed.md"), "markdown as before");
    }
}
