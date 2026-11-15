package ai.kubemoot.agent.chat;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.Optional;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.ValueSource;

/** The access the operator declares decides whether an agent stores its tool results. */
class WorkspaceAccessTest {

    @Test
    void nothingDeclaredIsImplicitAndStores() {
        assertEquals(WorkspaceAccess.IMPLICIT, WorkspaceAccess.of(Optional.empty()));
        assertEquals(WorkspaceAccess.IMPLICIT, WorkspaceAccess.of(Optional.of("  ")));
        assertTrue(WorkspaceAccess.IMPLICIT.storesResults());
    }

    @Test
    void theFourDeclaredValuesAreRead() {
        assertEquals(WorkspaceAccess.NONE, WorkspaceAccess.of(Optional.of("none")));
        assertEquals(WorkspaceAccess.READ, WorkspaceAccess.of(Optional.of("read")));
        assertEquals(WorkspaceAccess.WRITE, WorkspaceAccess.of(Optional.of("write")));
        assertEquals(WorkspaceAccess.READ_WRITE, WorkspaceAccess.of(Optional.of("readWrite")));
    }

    @ParameterizedTest
    @ValueSource(strings = {"READWRITE", "read-write", "read_write", " readWrite "})
    void spellingVariantsOfReadWriteAreAccepted(String value) {
        assertEquals(WorkspaceAccess.READ_WRITE, WorkspaceAccess.of(Optional.of(value)));
    }

    @ParameterizedTest
    @ValueSource(strings = {"writeOnly", "all", "true", "readwrite2"})
    void anUnknownValueKeepsTheImplicitBehaviorSoATypoCannotSilenceAnAgent(String value) {
        var access = WorkspaceAccess.of(Optional.of(value));
        assertEquals(WorkspaceAccess.IMPLICIT, access);
        assertTrue(access.storesResults());
    }

    @Test
    void onlyWriteAccessStoresResults() {
        assertFalse(WorkspaceAccess.NONE.storesResults());
        assertFalse(WorkspaceAccess.READ.storesResults());
        assertTrue(WorkspaceAccess.WRITE.storesResults());
        assertTrue(WorkspaceAccess.READ_WRITE.storesResults());
    }
}
