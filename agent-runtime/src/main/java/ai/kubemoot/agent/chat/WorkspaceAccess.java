package ai.kubemoot.agent.chat;

import java.util.Locale;
import java.util.Optional;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * What an agent may do with the discussion workspace, as the operator declares it in
 * KUBEMOOT_WORKSPACE_ACCESS. The runtime decides one thing from it: whether the agent stores its
 * tool results in the workspace. Reading is decided by the tool list the operator derives from the
 * same declaration, and the NATS permissions the operator grants decide whether a write could
 * succeed, so this class never lets an agent do more than those two allow.
 *
 * <p>{@link #IMPLICIT} is the access of an agent that declares nothing: it stores every result, as
 * before access could be declared.
 */
public enum WorkspaceAccess {
    /** Nothing declared: the agent behaves as it always has. */
    IMPLICIT,
    /** No workspace: nothing is stored, and a large result returns inline under the tool result budget. */
    NONE,
    /** The workspace read tools only: the agent's own results are not stored. */
    READ,
    /** Stores the agent's results and offers it no read tools. */
    WRITE,
    /** Stores the agent's results and offers it the read tools. */
    READ_WRITE;

    private static final Logger log = LoggerFactory.getLogger(WorkspaceAccess.class);

    /**
     * The access a declared value names. An absent or blank value is {@link #IMPLICIT}. A value
     * that is not one of none, read, write, readWrite is also {@link #IMPLICIT}, with a warning, so a
     * typo cannot silence an agent that used to store.
     */
    public static WorkspaceAccess of(Optional<String> declared) {
        String raw = declared.map(String::trim).orElse("");
        if (raw.isEmpty()) {
            return IMPLICIT;
        }
        return switch (raw.toLowerCase(Locale.ROOT).replace("-", "").replace("_", "")) {
            case "none" -> NONE;
            case "read" -> READ;
            case "write" -> WRITE;
            case "readwrite" -> READ_WRITE;
            default -> {
                log.warn("KUBEMOOT_WORKSPACE_ACCESS={} is not none, read, write or readWrite; the agent keeps its implicit access", raw);
                yield IMPLICIT;
            }
        };
    }

    /** True when the agent's tool results go to the workspace. */
    public boolean storesResults() {
        return this == IMPLICIT || this == WRITE || this == READ_WRITE;
    }
}
