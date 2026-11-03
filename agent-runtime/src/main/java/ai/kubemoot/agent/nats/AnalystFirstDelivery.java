package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import io.nats.client.Connection;
import java.util.List;

/**
 * The coordinator's side of the analyst-first delivery that needs the bus and the model windows: the
 * catalog of a thread's stored calls, which the analysts are shown and the synthesis covers against. The
 * coverage and the catalog themselves are {@link EvidenceCoverage} and {@link ThreadCatalog}.
 */
final class AnalystFirstDelivery {

    private final NatsConnectionProvider natsProvider;
    private final ChatService chatService;

    AnalystFirstDelivery(NatsConnectionProvider natsProvider, ChatService chatService) {
        this.natsProvider = natsProvider;
        this.chatService = chatService;
    }

    /** The catalog of the thread's stored calls; empty without a bus or with per-call artifacts off. */
    ThreadCatalog catalog(String threadId) {
        Connection conn = natsProvider.getConnection();
        if (conn == null || !chatService.perCallArtifactsOn()) {
            return new ThreadCatalog("", List.of());
        }
        return ThreadCatalog.load(conn, DiscussionArtifacts.threadPrefix(natsProvider.scope(), threadId));
    }

    /**
     * The complete list of the thread's stored tool calls, for the analysts the first review wakes; empty
     * when there are none or per-call artifacts are off. It is rendered one level coarser when it does not
     * fit a quarter of the smallest window, and never cut.
     */
    String catalogForReview(String threadId) {
        var catalog = catalog(threadId);
        return catalog.isEmpty() ? "" : catalog.render(ThreadCatalog.roomChars(chatService.simpleCallContextTokens()));
    }
}
