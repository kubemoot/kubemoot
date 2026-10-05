package ai.kubemoot.indexer.service;

import ai.kubemoot.indexer.config.IndexerConfig;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Component;

import java.sql.Array;
import java.sql.Connection;
import java.sql.PreparedStatement;
import java.sql.SQLException;
import java.util.Set;
import java.util.regex.Pattern;

/**
 * Prunes a pgvector table (named {@code data_<collection>}) down to the ids of the current run.
 */
@Component
public class JdbcStaleChunkPruner implements StaleChunkPruner {

    private static final Pattern SAFE_IDENTIFIER = Pattern.compile("[A-Za-z0-9_]+");

    private final JdbcTemplate jdbcTemplate;
    private final String tableName;

    public JdbcStaleChunkPruner(JdbcTemplate jdbcTemplate, IndexerConfig config) {
        this.jdbcTemplate = jdbcTemplate;
        this.tableName = tableFor(config.vectorstoreCollection());
    }

    static String tableFor(String collection) {
        if (collection == null || !SAFE_IDENTIFIER.matcher(collection).matches()) {
            throw new IllegalArgumentException("Invalid vector store collection name: " + collection);
        }
        return "data_" + collection;
    }

    String tableName() {
        return tableName;
    }

    @Override
    public int pruneExcept(Set<String> keepIds) {
        String sql = "DELETE FROM " + tableName + " WHERE NOT (id::text = ANY (?))";
        return jdbcTemplate.update(connection -> prepare(connection, sql, keepIds));
    }

    private static PreparedStatement prepare(Connection connection, String sql, Set<String> keepIds)
            throws SQLException {
        Array ids = connection.createArrayOf("text", keepIds.toArray());
        PreparedStatement statement = connection.prepareStatement(sql);
        statement.setArray(1, ids);
        return statement;
    }
}
