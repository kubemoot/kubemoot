package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import io.nats.client.Connection;

/**
 * The coordinator's list of every file stored in a thread, given to the agents the review wakes
 * (they can read files with workspace_query and the artifact tools). The list itself is
 * {@link ThreadCatalog}; this class loads it from the bus and sizes it to the model windows.
 */
final class ReviewCatalog {

    private final NatsConnectionProvider natsProvider;
    private final ChatService chatService;

    ReviewCatalog(NatsConnectionProvider natsProvider, ChatService chatService) {
        this.natsProvider = natsProvider;
        this.chatService = chatService;
    }

    /**
     * The complete list of the thread's stored files for the first review; empty when there are
     * none or there is no bus. It is rendered one level coarser when it does not fit a quarter of the
     * smallest window, and never cut.
     */
    String forReview(String threadId) {
        Connection conn = natsProvider.getConnection();
        if (conn == null) {
            return "";
        }
        var catalog = ThreadCatalog.load(conn, DiscussionArtifacts.threadPrefix(natsProvider.scope(), threadId));
        return catalog.isEmpty() ? "" : catalog.render(ThreadCatalog.roomChars(chatService.simpleCallContextTokens()));
    }
}
