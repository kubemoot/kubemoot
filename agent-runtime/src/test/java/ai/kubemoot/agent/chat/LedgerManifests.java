package ai.kubemoot.agent.chat;

import java.time.Instant;

/** Builds the manifest a {@link ToolCallLedger} writes for given results, for tests in other packages. */
public final class LedgerManifests {

    private LedgerManifests() {}

    /** The manifest of one agent whose successful calls returned {@code results}, in order. */
    public static String of(String... results) {
        var ledger = new ToolCallLedger(new ChatServiceCallFilesTest.FakeStore(), "th", "k8s");
        for (String result : results) {
            ledger.record(new ToolCallLedger.Call("workspace_grep", "id", "{}", Instant.EPOCH, 1), result, false, 1_000_000);
        }
        return ledger.manifest();
    }
}
