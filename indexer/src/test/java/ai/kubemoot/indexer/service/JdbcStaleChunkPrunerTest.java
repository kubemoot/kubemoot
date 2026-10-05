package ai.kubemoot.indexer.service;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.junit.jupiter.api.Test;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.jdbc.core.PreparedStatementCreator;

import java.sql.Array;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.mockito.ArgumentCaptor.forClass;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;

class JdbcStaleChunkPrunerTest {

    private static IndexerConfig configFor(String collection) {
        IndexerConfig config = mock(IndexerConfig.class);
        when(config.vectorstoreCollection()).thenReturn(collection);
        return config;
    }

    @Test
    void tableNameIsDataPlusCollection() {
        assertEquals("data_kubectl_reference",
            new JdbcStaleChunkPruner(mock(JdbcTemplate.class), configFor("kubectl_reference")).tableName());
    }

    @Test
    void rejectsUnsafeCollectionNames() {
        JdbcTemplate template = mock(JdbcTemplate.class);

        assertThrows(IllegalArgumentException.class,
            () -> new JdbcStaleChunkPruner(template, configFor("x; DROP TABLE y")));
        assertThrows(IllegalArgumentException.class,
            () -> new JdbcStaleChunkPruner(template, configFor("has-hyphen")));
        assertThrows(IllegalArgumentException.class,
            () -> new JdbcStaleChunkPruner(template, configFor(null)));
    }

    @Test
    void deletesRowsOutsideTheKeepSetInTheCollectionTable() throws Exception {
        JdbcTemplate template = mock(JdbcTemplate.class);
        when(template.update(org.mockito.ArgumentMatchers.any(PreparedStatementCreator.class))).thenReturn(4);
        JdbcStaleChunkPruner pruner = new JdbcStaleChunkPruner(template, configFor("helm_reference"));

        int removed = pruner.pruneExcept(Set.of("id-1"));

        assertEquals(4, removed);
        var captor = forClass(PreparedStatementCreator.class);
        verify(template).update(captor.capture());
        Connection connection = mock(Connection.class);
        PreparedStatement statement = mock(PreparedStatement.class);
        Array array = mock(Array.class);
        when(connection.createArrayOf("text", new Object[]{"id-1"})).thenReturn(array);
        when(connection.prepareStatement("DELETE FROM data_helm_reference WHERE NOT (id::text = ANY (?))"))
            .thenReturn(statement);
        captor.getValue().createPreparedStatement(connection);
        verify(statement).setArray(1, array);
    }
}
