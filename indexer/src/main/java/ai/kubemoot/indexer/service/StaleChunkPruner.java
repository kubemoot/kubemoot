package ai.kubemoot.indexer.service;

import java.util.Set;

/**
 * Removes the rows of a collection that a fresh indexing run did not produce.
 */
public interface StaleChunkPruner {

    /**
     * Deletes every row of the collection whose id is not in {@code keepIds}.
     *
     * @param keepIds ids written by the current run
     * @return number of rows removed
     */
    int pruneExcept(Set<String> keepIds);
}
