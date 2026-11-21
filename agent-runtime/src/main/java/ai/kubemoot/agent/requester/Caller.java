package ai.kubemoot.agent.requester;

import java.util.LinkedHashMap;
import java.util.Map;

/**
 * Whose question a model turn is answering: the conversation and the principal that asked it.
 * {@link CallerScope} binds it around a turn; the tool client puts {@link #toolMeta()} in the MCP
 * {@code _meta} of every tool call, so a tool server can tell which person's access a call
 * stands for. The runtime sets these keys from what the coordinator put on the thread; the model
 * only ever supplies a call's arguments.
 *
 * <p>No token is held here or anywhere in this runtime: the principal id is a handle, and a
 * credential reaches a tool only through the gateway that looks it up for that handle.
 */
public record Caller(String conversationId, Principal principal) {

    /** The {@code _meta} key that names the conversation. */
    public static final String META_CONVERSATION_ID = "conversationId";
    /** The {@code _meta} key that names the asking principal, set only for a principal that has an id. */
    public static final String META_PRINCIPAL_ID = "principalId";

    /** True when there is nothing to send: no conversation and no principal id. */
    public boolean isEmpty() {
        return blank(conversationId) && (principal == null || blank(principal.id()));
    }

    /**
     * The tool call's {@code _meta} entries: the conversation id when known, and the principal id
     * when the principal has one (an anonymous caller has none, so it sends only the conversation).
     */
    public Map<String, Object> toolMeta() {
        var meta = new LinkedHashMap<String, Object>();
        if (!blank(conversationId)) {
            meta.put(META_CONVERSATION_ID, conversationId);
        }
        if (principal != null && !blank(principal.id())) {
            meta.put(META_PRINCIPAL_ID, principal.id());
        }
        return meta;
    }

    private static boolean blank(String value) {
        return value == null || value.isBlank();
    }
}
