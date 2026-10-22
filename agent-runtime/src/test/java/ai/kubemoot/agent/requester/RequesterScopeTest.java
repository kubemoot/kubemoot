package ai.kubemoot.agent.requester;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class RequesterScopeTest {

    private static final Requester NEW_YORK = new Requester("America/New_York", null);
    private static final Requester TOKYO = new Requester("Asia/Tokyo", null);

    @Test
    void nothingIsBoundByDefault() {
        assertTrue(RequesterScope.current().isEmpty());
    }

    @Test
    void theBoundRequesterIsVisibleUntilTheScopeCloses() {
        try (var scope = RequesterScope.bind(NEW_YORK)) {
            assertEquals(NEW_YORK, RequesterScope.current().orElseThrow());
        }
        assertTrue(RequesterScope.current().isEmpty());
    }

    @Test
    void nestedScopesRestoreTheOuterRequester() {
        try (var outer = RequesterScope.bind(NEW_YORK)) {
            try (var inner = RequesterScope.bind(TOKYO)) {
                assertEquals(TOKYO, RequesterScope.current().orElseThrow());
            }
            assertEquals(NEW_YORK, RequesterScope.current().orElseThrow());
        }
    }

    @Test
    void bindingNullClearsTheRequesterForTheScope() {
        try (var outer = RequesterScope.bind(NEW_YORK)) {
            try (var inner = RequesterScope.bind(null)) {
                assertTrue(RequesterScope.current().isEmpty());
            }
            assertEquals(NEW_YORK, RequesterScope.current().orElseThrow());
        }
    }
}
