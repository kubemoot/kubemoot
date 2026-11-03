package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.nats.client.Connection;
import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.function.Supplier;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

/**
 * The coordinator's side of the analyst-first delivery that needs the bus and the model windows: the
 * catalog of a thread's stored calls, the decision whether analysts are convened for the results none
 * of them read, and the task each convened analyst is given. The coverage, the plan and the task text
 * themselves are {@link EvidenceCoverage}, {@link DeliveryPlan} and {@link AnalystReadouts}.
 */
final class AnalystFirstDelivery {

    private static final Logger log = LoggerFactory.getLogger(AnalystFirstDelivery.class);

    private final NatsConnectionProvider natsProvider;
    private final ChatService chatService;
    private final ObjectMapper mapper;
    private final Supplier<String> systemPrompt;
    private final Supplier<String> crewResumes;

    AnalystFirstDelivery(NatsConnectionProvider natsProvider, ChatService chatService, ObjectMapper mapper,
                         Supplier<String> systemPrompt, Supplier<String> crewResumes) {
        this.natsProvider = natsProvider;
        this.chatService = chatService;
        this.mapper = mapper;
        this.systemPrompt = systemPrompt;
        this.crewResumes = crewResumes;
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

    /**
     * Decides whether the stored results no analyst read go to the synthesizer or to analysts first. They
     * go to the synthesizer when it holds all of them in one round, the test the synthesis itself uses
     * ({@link StoredDelivery#fitsOneRound}). The room is the smallest window among the servers that hold
     * the model, less the prompt as it stands: the server the scheduler picks later is not known yet.
     */
    DeliveryPlan plan(StoredDelivery delivery, int convenings) {
        if (delivery.catalog().isEmpty()) {
            return DeliveryPlan.synthesize();
        }
        var room = chatService.simpleCallRoomTokens(systemPrompt.get(), delivery.conversation());
        boolean fits = delivery.fitsOneRound(chatService::estimateTokens, room);
        var plan = DeliveryPlan.decide(delivery.coverage().uncovered(), fits, queryAnalysts(), convenings);
        if (plan.convenes()) {
            log.info("stored results no analyst read take {} tokens and one synthesis round holds {}; convening",
                    delivery.uncoveredTokens(chatService::estimateTokens), room.isPresent() ? room.getAsLong() : -1);
        }
        return plan;
    }

    /** The crew's analysts that have a tool to run a query or code. */
    List<String> queryAnalysts() {
        try {
            String raw = crewResumes.get();
            return raw == null || raw.isBlank() ? List.of() : AnalystReadouts.queryCapable(mapper.readTree(raw));
        } catch (Exception e) {
            log.debug("Could not read the analysts' tools from the resumes: {}", e.getMessage());
            return List.of();
        }
    }

    /**
     * The task of each analyst in {@code assignments}: the question and its patterns with the stored calls
     * not yet read, as they stand in the thread's catalog now (a restarted coordinator builds the same).
     */
    Map<String, String> secondReviewTasks(String threadId, String question,
                                          List<DeliveryPlan.Assignment> assignments,
                                          Map<String, Set<String>> readKeys, Collection<String> dataAnalysts) {
        var coverage = EvidenceCoverage.compute(catalog(threadId), readKeys, dataAnalysts);
        return AnalystReadouts.secondReviewTasks(question, assignments, coverage.uncovered());
    }
}
