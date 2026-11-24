package ai.kubemoot.agent.chat;

import java.time.Instant;

/** Builds the manifest a {@link ToolCallLedger} writes for given results, for tests in other packages. */
public final class LedgerManifests {

    private LedgerManifests() {}

    /** The manifest of one agent that stores nothing, whose successful calls returned {@code results}. */
    public static String unstored(String... results) {
        return unstored(java.util.OptionalLong.empty(), results);
    }

    /** The same, for readers whose context window is {@code window}: the room for posted results follows it. */
    public static String unstored(java.util.OptionalLong window, String... results) {
        var ledger = ToolCallLedger.unstoredForLoop("th", "obs", window);
        for (String result : results) {
            ledger.recordCall(new ToolCallLedger.Call("artifact_select", "id", "{}", Instant.EPOCH, 1), result, false, 1_000_000);
        }
        return ledger.manifest();
    }

    /** The manifest of one agent whose successful calls returned {@code results}, in order. */
    public static String of(String... results) {
        var ledger = new ToolCallLedger(new ChatServiceCallFilesTest.FakeStore(), "th", "k8s");
        for (String result : results) {
            ledger.recordCall(new ToolCallLedger.Call("workspace_grep", "id", "{}", Instant.EPOCH, 1), result, false, 1_000_000);
        }
        return ledger.manifest();
    }
}
