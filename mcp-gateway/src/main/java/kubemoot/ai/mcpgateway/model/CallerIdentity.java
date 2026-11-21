package kubemoot.ai.mcpgateway.model;

import java.util.Map;
import java.util.regex.Pattern;

/**
 * Who a tool call is for: the conversation and the pseudonymous principal the agent-runtime sets
 * in the call's {@code _meta}. Both are ids, never a token. They are read from {@code _meta}
 * only (the model sets {@code arguments}, not {@code _meta}), so an identity-like key inside the
 * arguments is never consulted.
 */
public record CallerIdentity(String conversationId, String principalId) {

    public static final String CONVERSATION_KEY = "conversationId";
    public static final String PRINCIPAL_KEY = "principalId";

    private static final Pattern ID = Pattern.compile("[A-Za-z0-9._:@/+=-]{1,256}");

    /** The identity in {@code meta}, or null when either id is missing or malformed. */
    public static CallerIdentity from(Object meta) {
        if (!(meta instanceof Map<?, ?> map)) {
            return null;
        }
        String conversation = id(map.get(CONVERSATION_KEY));
        String principal = id(map.get(PRINCIPAL_KEY));
        return conversation == null || principal == null ? null : new CallerIdentity(conversation, principal);
    }

    private static String id(Object value) {
        return value instanceof String text && ID.matcher(text).matches() ? text : null;
    }
}
