package ai.kubemoot.agent.signal;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import dev.langchain4j.agent.tool.ToolSpecification;
import dev.langchain4j.model.chat.request.json.JsonObjectSchema;

import java.io.IOException;
import java.util.ArrayList;
import java.util.List;

/**
 * The built-in {@code signal} tool: how an agent records its verdict in a discussion. Its schema
 * is generated from the archetype's declared kinds, so the archetype, not a prompt and not the
 * runtime, defines what an agent can say. The agent's reply text stays its finding; the call is
 * its verdict.
 */
public final class SignalTool {

    /** The tool's name, as the model calls it. */
    public static final String NAME = "signal";

    static final String KIND = "kind";
    static final String TO = "to";
    static final String STATEMENT = "statement";
    static final String REASON = "reason";
    static final String EVIDENCE = "evidence";

    /** The most characters kept of any one argument. */
    static final int MAX_ARGUMENT_CHARS = 2000;

    private static final ObjectMapper MAPPER = new ObjectMapper();

    private final DeclaredSignals signals;
    private final ToolSpecification spec;

    public SignalTool(DeclaredSignals signals) {
        this.signals = signals;
        this.spec = buildSpec(signals);
    }

    /** The tool as it is offered to the model. */
    public ToolSpecification spec() {
        return spec;
    }

    /** What one call did: the text the model is answered with, and the verdict when the call was valid. */
    public record Outcome(String response, SignalCall call) {
        public boolean recorded() {
            return call != null;
        }
    }

    /**
     * Reads one call's arguments. A call with an undeclared kind, or an addressed kind without
     * {@code to}, is answered with what to fix and records nothing, so the model can call again.
     */
    public Outcome record(String arguments) {
        JsonNode args = parse(arguments);
        String kind = arg(args, KIND);
        DeclaredSignal declared = signals.find(kind);
        if (declared == null) {
            return new Outcome(error("kind must be one of " + names() + ", not '" + kind + "'"), null);
        }
        String to = arg(args, TO);
        if (declared.addressed() && to.isEmpty()) {
            return new Outcome(error("a " + kind + " is said to one agent: name it in " + TO), null);
        }
        var call = new SignalCall(kind, declared.addressed() ? to : "", arg(args, STATEMENT), arg(args, REASON),
                arg(args, EVIDENCE));
        return new Outcome(confirmation(call), call);
    }

    private static String confirmation(SignalCall call) {
        var answer = MAPPER.createObjectNode().put("recorded", call.kind());
        if (call.effect() == SignalEffect.QUESTION) {
            answer.put(TO, call.to()).put("next", "the agent rechecks and answers; you are then asked to signal again");
        }
        return answer.toString();
    }

    private static String error(String message) {
        return MAPPER.createObjectNode().put("error", message).toString();
    }

    private static JsonNode parse(String arguments) {
        try {
            JsonNode node = arguments == null || arguments.isBlank() ? null : MAPPER.readTree(arguments);
            return node == null ? MAPPER.createObjectNode() : node;
        } catch (IOException e) {
            return MAPPER.createObjectNode();
        }
    }

    private static String arg(JsonNode args, String field) {
        String value = args.path(field).asText("").strip();
        return value.length() > MAX_ARGUMENT_CHARS ? value.substring(0, MAX_ARGUMENT_CHARS) : value;
    }

    private String names() {
        return String.join(", ", signals.all().stream().map(DeclaredSignal::name).toList());
    }

    private static ToolSpecification buildSpec(DeclaredSignals signals) {
        List<String> kinds = new ArrayList<>();
        List<String> addressed = new ArrayList<>();
        var description = new StringBuilder("Record your verdict on the discussion. Call it when your work is done, "
                + "with the kind that fits; your reply text stays your finding. Kinds:");
        for (DeclaredSignal s : signals.all()) {
            kinds.add(s.name());
            if (s.addressed()) {
                addressed.add(s.name());
            }
            description.append("\n- ").append(s.name()).append(": ").append(s.meaning());
        }
        var parameters = JsonObjectSchema.builder()
                .addEnumProperty(KIND, kinds, "The kind of verdict, one of the kinds above.")
                .addStringProperty(STATEMENT, "The claim, concern or question, in a sentence or two.")
                .addStringProperty(REASON, "Why, from your results.")
                .addStringProperty(EVIDENCE, "Optional: a stored-result key, or a short quote from a result.");
        if (!addressed.isEmpty()) {
            parameters.addStringProperty(TO, "The agent the signal is said to, by name. Required for: "
                    + String.join(", ", addressed) + ".");
        }
        return ToolSpecification.builder()
                .name(NAME)
                .description(description.toString())
                .parameters(parameters.required(KIND, STATEMENT).build())
                .build();
    }
}
