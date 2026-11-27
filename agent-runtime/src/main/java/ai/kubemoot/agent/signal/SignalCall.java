package ai.kubemoot.agent.signal;

/**
 * One call of the signal tool: the agent's verdict. {@code kind} is a declared name; {@code to}
 * names the agent an addressed kind is said to (empty otherwise); {@code statement} is the claim,
 * concern or question; {@code reason} says why; {@code evidence} is a stored-result key or a short
 * quote, or empty. Every text is stripped, never null.
 */
public record SignalCall(String kind, String to, String statement, String reason, String evidence) {

    public SignalCall {
        kind = text(kind);
        to = text(to);
        statement = text(statement);
        reason = text(reason);
        evidence = text(evidence);
    }

    /** What the runtime does with this call's kind, from its table. */
    public SignalEffect effect() {
        return SignalEffect.of(kind);
    }

    /**
     * The statement with its reason and evidence, as the published content leads with them:
     * "statement", then "Reason: ..." and "Evidence: ..." on their own lines when given.
     */
    public String stated() {
        var sb = new StringBuilder(statement);
        appendLine(sb, "Reason: ", reason);
        appendLine(sb, "Evidence: ", evidence);
        return sb.toString();
    }

    private static void appendLine(StringBuilder sb, String label, String value) {
        if (value.isEmpty()) {
            return;
        }
        if (!sb.isEmpty()) {
            sb.append('\n');
        }
        sb.append(label).append(value);
    }

    private static String text(String value) {
        return value == null ? "" : value.strip();
    }
}
