package ai.kubemoot.agent.signal;

import dev.langchain4j.agent.tool.ToolSpecification;

import java.util.Optional;
import java.util.concurrent.atomic.AtomicReference;

/**
 * The signal tool for one discussion turn, bound to the thread that runs the turn. While it is
 * bound, the tool loop offers the {@code signal} tool next to the agent's MCP tools and records
 * its calls here; the caller reads the verdict after the loop. Bound with try-with-resources, so
 * the tool is never offered outside the turn.
 */
public final class SignalScope implements AutoCloseable {

    private static final ThreadLocal<SignalScope> CURRENT = new ThreadLocal<>();
    private static final SignalScope INACTIVE = new SignalScope(null, null);

    private final SignalTool tool;
    private final SignalScope previous;
    private final AtomicReference<SignalCall> last = new AtomicReference<>();

    private SignalScope(SignalTool tool, SignalScope previous) {
        this.tool = tool;
        this.previous = previous;
    }

    /**
     * Binds the tool for {@code signals} to this thread. When the archetype declares no kind the
     * returned scope is inactive: nothing is offered and {@link #call()} stays empty.
     */
    public static SignalScope bind(DeclaredSignals signals) {
        if (signals == null || signals.isEmpty()) {
            return INACTIVE;
        }
        var scope = new SignalScope(new SignalTool(signals), CURRENT.get());
        CURRENT.set(scope);
        return scope;
    }

    /** The scope bound to this thread, or null when no turn offers the tool. */
    public static SignalScope current() {
        return CURRENT.get();
    }

    /** The tool's specification, for the list offered to the model. */
    public ToolSpecification spec() {
        return tool.spec();
    }

    /** True when {@code toolName} is the signal tool this scope handles. */
    public boolean handles(String toolName) {
        return tool != null && SignalTool.NAME.equals(toolName);
    }

    /** Runs one call: a valid call replaces the verdict recorded before it. Returns the model's answer. */
    public String handle(String arguments) {
        var outcome = tool.accept(arguments);
        if (outcome.recorded()) {
            last.set(outcome.call());
        }
        return outcome.response();
    }

    /** The last valid call of the turn: the agent's verdict. Empty when it made none. */
    public Optional<SignalCall> call() {
        return Optional.ofNullable(last.get());
    }

    /** True when the verdict says the agent has no finding (no data, a missing tool, nothing to add). */
    public boolean declaresNoFinding() {
        return call().map(c -> c.effect().withoutFinding()).orElse(false);
    }

    @Override
    public void close() {
        if (tool == null) {
            return;
        }
        if (previous == null) {
            CURRENT.remove();
        } else {
            CURRENT.set(previous);
        }
    }
}
