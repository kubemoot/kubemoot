package ai.kubemoot.agent.provider;

/**
 * Encodes free-form names (model names such as {@code qwen2.5:14b}, agent ids)
 * into NATS KV key tokens. Letters, digits and '-' pass through; every other
 * character becomes '_' and two hex digits (or '__' and four for characters above
 * 0xff), so distinct names never
 * share a token, and a token never contains the '.' token separator or ':'.
 */
public final class ModelKeys {

    private ModelKeys() {}

    /** The KV-safe token for {@code name}. */
    public static String token(String name) {
        if (name == null || name.isEmpty()) {
            return "_";
        }
        StringBuilder sb = new StringBuilder(name.length() + 8);
        for (char c : name.toCharArray()) {
            if (isPlain(c)) {
                sb.append(c);
            } else if (c <= 0xff) {
                sb.append('_').append(String.format("%02x", (int) c));
            } else {
                sb.append("__").append(String.format("%04x", (int) c));
            }
        }
        return sb.toString();
    }

    private static boolean isPlain(char c) {
        return c < 128 && (Character.isLetterOrDigit(c) || c == '-');
    }
}
