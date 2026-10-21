package ai.kubemoot.indexer.service;

import org.junit.jupiter.api.Test;
import org.springframework.jdbc.core.JdbcTemplate;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;

class CollectionDropperTest {

    private final JdbcTemplate template = mock(JdbcTemplate.class);
    private final CollectionDropper dropper = new CollectionDropper(template);

    @Test
    void dropsEachRetiredCollectionTable() {
        int handled = dropper.drop(List.of("docs", "docs_old_1"), "docs_new");

        assertEquals(2, handled);
        verify(template).execute("DROP TABLE IF EXISTS data_docs");
        verify(template).execute("DROP TABLE IF EXISTS data_docs_old_1");
    }

    @Test
    void neverDropsTheCollectionInUse() {
        assertThrows(IllegalArgumentException.class, () -> dropper.drop(List.of("docs", "docs_new"), "docs_new"));

        verify(template, never()).execute(anyString());
    }

    @Test
    void rejectsUnsafeNamesBeforeDroppingAnything() {
        assertThrows(IllegalArgumentException.class, () -> dropper.drop(List.of("docs", "x; DROP TABLE y"), "docs_new"));
        assertThrows(IllegalArgumentException.class, () -> dropper.drop(List.of("has-hyphen"), "docs_new"));

        verify(template, never()).execute(anyString());
    }

    @Test
    void nothingToDropDoesNothing() {
        assertEquals(0, dropper.drop(List.of(), "docs"));

        verify(template, never()).execute(anyString());
    }
}
