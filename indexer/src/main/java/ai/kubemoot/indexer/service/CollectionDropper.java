package ai.kubemoot.indexer.service;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.jdbc.core.JdbcTemplate;
import org.springframework.stereotype.Component;

import java.util.List;
import java.util.regex.Pattern;

/**
 * Deletes collections (pgvector tables named {@code data_<collection>}) that no reader uses
 * any more, such as an index replaced by one built with another embedding model.
 */
@Component
public class CollectionDropper {

    private static final Logger logger = LoggerFactory.getLogger(CollectionDropper.class);
    private static final Pattern SAFE_IDENTIFIER = Pattern.compile("[A-Za-z0-9_]+");

    private final JdbcTemplate jdbcTemplate;

    public CollectionDropper(JdbcTemplate jdbcTemplate) {
        this.jdbcTemplate = jdbcTemplate;
    }

    /**
     * Deletes the collections, never the one in use.
     *
     * @param collections collections to delete
     * @param inUse the collection readers use now; asking to delete it is an error
     * @return the number of collections handled
     */
    public int drop(List<String> collections, String inUse) {
        for (String collection : collections) {
            if (collection.equals(inUse)) {
                throw new IllegalArgumentException("Refusing to delete the collection in use: " + collection);
            }
            if (!SAFE_IDENTIFIER.matcher(collection).matches()) {
                throw new IllegalArgumentException("Invalid vector store collection name: " + collection);
            }
        }
        for (String collection : collections) {
            jdbcTemplate.execute("DROP TABLE IF EXISTS data_" + collection);
            logger.info("Deleted collection '{}'", collection);
        }
        return collections.size();
    }
}
