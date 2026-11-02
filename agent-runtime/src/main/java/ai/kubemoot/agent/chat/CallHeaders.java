package ai.kubemoot.agent.chat;

/**
 * The NATS object headers on a per-call workspace object. Names that the OpenTelemetry GenAI
 * semantic conventions define use the convention's name; the rest sit in the {@code kubemoot.}
 * namespace. Header keys are case-sensitive.
 */
public final class CallHeaders {

    /** Always {@code execute_tool}, the GenAI operation name for a tool call. */
    public static final String OPERATION_NAME = "gen_ai.operation.name";
    public static final String OPERATION_EXECUTE_TOOL = "execute_tool";
    public static final String TOOL_NAME = "gen_ai.tool.name";
    /** The model's tool call id, or the object's call number and tool when the model sent none. */
    public static final String TOOL_CALL_ID = "gen_ai.tool.call.id";
    /** The call's arguments as the JSON the model sent, cut at {@link #MAX_ARGUMENTS_CHARS}. */
    public static final String TOOL_CALL_ARGUMENTS = "gen_ai.tool.call.arguments";
    public static final String AGENT_NAME = "gen_ai.agent.name";
    /** Present only on an error call; the value is {@link #ERROR_TYPE_TOOL}. */
    public static final String ERROR_TYPE = "error.type";
    public static final String ERROR_TYPE_TOOL = "tool_error";

    /** When the call started, RFC 3339 UTC. */
    public static final String STARTED = "kubemoot.started";
    public static final String DURATION_MS = "kubemoot.duration_ms";
    /** {@code true} or {@code false}: whether the result is a tool error. */
    public static final String ERROR = "kubemoot.error";
    /** Size of the stored body in bytes. */
    public static final String BYTES = "kubemoot.bytes";
    /** Row or item count; absent when the runtime cannot tell (a text result). */
    public static final String ROWS = "kubemoot.rows";
    /** {@code json} when the body is a JSON object or list, otherwise {@code text}. */
    public static final String FORMAT = "kubemoot.format";
    public static final String FORMAT_JSON = "json";
    public static final String FORMAT_TEXT = "text";
    /** {@code true} only when the result was cut at the size cap; the body then ends with the cut marker. */
    public static final String TRUNCATED = "kubemoot.truncated";

    /** Most characters of the arguments kept in {@link #TOOL_CALL_ARGUMENTS}. */
    public static final int MAX_ARGUMENTS_CHARS = 1_000;

    private CallHeaders() {}
}
