package ai.kubemoot.agent.nats;

import ai.kubemoot.agent.chat.ChatService;
import ai.kubemoot.agent.config.AgentProperties;
import com.fasterxml.jackson.databind.JsonNode;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;
import java.util.concurrent.CopyOnWriteArrayList;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.Mockito.when;

/**
 * A whole thread through RATIFY: the synthesis is a draft the contributors check, the coordinator
 * revises once on their objections, and the answer is released when the roster has ratified or the
 * rounds are used up, with any objection that stands attached.
 */
class RatifyThreadTest extends CoordinatorThreadTestBase {

    private static final String THREAD = "t-ratify";
    private static final String METRICS = "k8s-metrics";
    private static final String WORKLOADS = "k8s-workloads";
    private static final String ANALYST = "k8s-analyst";
    private static final List<String> ROSTER = List.of(METRICS, WORKLOADS, ANALYST);
    private static final List<String> SORTED_ROSTER = ROSTER.stream().sorted().toList();
    private static final String DRAFT = "Eleven pods.";
    private static final String REVISION = "Eleven pods, none on a node that does not exist.";
    private static final String OBJECTION = "the draft names crew-node-01, which my results do not hold";

    private volatile String ratify = "true";
    private volatile int maxRounds = 2;
    private final List<String> revisionPrompts = new CopyOnWriteArrayList<>();

    @Override
    protected boolean withBus() {
        return true;
    }

    @Override
    protected String resumesJson() {
        return """
                [{"name":"k8s-metrics","description":"Kubernetes metrics tooler","tools":["pods_top"]},
                 {"name":"k8s-workloads","description":"Kubernetes workloads tooler","tools":["resources_list"]},
                 {"name":"k8s-analyst","role":"analyst","description":"Reads what the toolers gathered","tools":[]}]
                """;
    }

    @Override
    protected void configureDiscuss(AgentProperties.Discuss discuss) {
        when(discuss.ratifyEnabled()).thenReturn(Optional.of(ratify));
        when(discuss.ratifyMaxRounds()).thenReturn(Optional.of(maxRounds));
        when(chatService.simpleLlmCallWithTokens(any(), anyString())).thenAnswer(call -> {
            String prompt = call.getArgument(1);
            if (prompt.contains("[OBJECTIONS]")) {
                revisionPrompts.add(prompt);
                return new ChatService.SimpleLlmResult(REVISION, 100, 20);
            }
            return new ChatService.SimpleLlmResult(prompt.contains("Discussion thread") ? DRAFT : selection, 100, 20);
        });
    }

    /** Runs the thread to the draft on the table: evidence, review, synthesis. */
    private void toRatify() throws Exception {
        send(THREAD, "human", "thread_start", "Are all the cluster nodes healthy?",
                "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(THREAD);
        send(THREAD, METRICS, "agree", "14 nodes, all Ready", null);
        send(THREAD, WORKLOADS, "agree", "no node reports pressure", null);
        settle(THREAD, EVALUATION_TO_REVIEW);
        send(THREAD, ANALYST, "agree", "14 of 14 Ready", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);
        await(() -> !publishedOfType("draft").isEmpty(), "no draft was published: " + timeline);
    }

    /** The metadata a reply to the draft of {@code round} carries: the round and the draft's own id. */
    private String ratifies(int round) {
        String id = publishedOfType("draft").get(round - 1).body().path("metadata").path("draftId").asText();
        return "{\"ratifies\":" + round + ",\"draftId\":\"" + id + "\"}";
    }

    private void answer(String agent, String verdict, String content, int round) throws Exception {
        send(THREAD, agent, verdict, content, ratifies(round));
    }

    private void everyoneAgrees(int round) throws Exception {
        for (String agent : ROSTER) {
            answer(agent, "agree", "checked", round);
        }
    }

    private DiscussionOrchestrator.ThreadState state() {
        return orchestrator.threadForTest(THREAD);
    }

    private void settleRatification() throws Exception {
        settle(THREAD, "ratify");
    }

    private JsonNode theAnswer() throws Exception {
        await(() -> !publishedOfType("synthesis").isEmpty(), "no answer was published: " + timeline);
        return publishedOfType("synthesis").get(0).body();
    }

    @Test
    void theSynthesisIsADraftPutToTheContributorsBeforeItIsTheAnswer() throws Exception {
        toRatify();

        JsonNode draft = publishedOfType("draft").get(0).body();
        assertEquals(DiscussionOrchestrator.Phase.RATIFY, state().phase);
        assertEquals(DRAFT, draft.path("content").asText());
        assertEquals(1, draft.path("metadata").path("round").asInt());
        assertEquals(2, draft.path("metadata").path("maxRounds").asInt());
        assertEquals(SORTED_ROSTER, MAPPER.convertValue(draft.path("metadata").path("roster"), List.class));
        assertEquals(SORTED_ROSTER, MAPPER.convertValue(draft.path("metadata").path("innerCircle"), List.class),
                "only the roster wakes on the draft");
        assertTrue(publishedOfType("synthesis").isEmpty(), "nobody has seen an answer yet");
        assertTrue(forThread(THREAD).stream().noneMatch(e -> e.type().equals("synthesis_done")),
                "the answer is logged when the phase closes");
    }

    @Test
    void whenEveryoneAgreesTheDraftIsTheAnswerAndTheThreadCloses() throws Exception {
        toRatify();
        everyoneAgrees(1);
        settleRatification();

        JsonNode answer = theAnswer();
        assertEquals(DRAFT, answer.path("content").asText());
        assertEquals("ratified", answer.path("metadata").path("ratification").path("state").asText());
        assertEquals(1, answer.path("metadata").path("ratification").path("rounds").asInt());
        await(() -> state().phase == DiscussionOrchestrator.Phase.CLOSED, "the thread did not close");
        assertEquals(1, publishedOfType("draft").size(), "no revision when nobody objected");
    }

    @Test
    void aConcernMakesTheCoordinatorReviseOnceAndPutTheRevisionToTheTable() throws Exception {
        toRatify();
        answer(METRICS, "agree", "checked", 1);
        answer(WORKLOADS, "concern", OBJECTION, 1);
        answer(ANALYST, "agree", "checked", 1);
        settleRatification();
        await(() -> publishedOfType("draft").size() == 2, "no revision was published: " + timeline);

        JsonNode revision = publishedOfType("draft").get(1).body();
        assertEquals(REVISION, revision.path("content").asText());
        assertEquals(2, revision.path("metadata").path("round").asInt());
        assertEquals(WORKLOADS, revision.path("metadata").path("objections").get(0).path("agent").asText());
        assertTrue(revisionPrompts.get(0).contains(OBJECTION), "the objection is in front of the coordinator");
        assertTrue(revisionPrompts.get(0).contains(DRAFT), "and the draft it answers");

        everyoneAgrees(2);
        settleRatification();
        JsonNode answer = theAnswer();
        assertEquals(REVISION, answer.path("content").asText(), "the revision is the answer");
        assertEquals("ratified", answer.path("metadata").path("ratification").path("state").asText());
        assertEquals(OBJECTION, answer.path("metadata").path("ratification").path("earlierObjections")
                .get(0).path("content").asText(), "what was raised in round 1 stays visible");
    }

    @Test
    void anObjectionThatStandsAfterTheLastRoundIsReleasedWithTheAnswer() throws Exception {
        toRatify();
        answer(METRICS, "block", OBJECTION, 1);
        answer(WORKLOADS, "agree", "checked", 1);
        answer(ANALYST, "agree", "checked", 1);
        settleRatification();
        await(() -> publishedOfType("draft").size() == 2, "no revision was published: " + timeline);
        answer(METRICS, "block", "still names a node that is not there", 2);
        answer(WORKLOADS, "agree", "checked", 2);
        answer(ANALYST, "agree", "checked", 2);
        settleRatification();

        JsonNode answer = theAnswer();
        String text = answer.path("content").asText();
        assertTrue(text.startsWith(REVISION), text);
        assertTrue(text.contains("Objections that stand"), text);
        assertTrue(text.contains("k8s-metrics (block): still names a node that is not there"), text);
        JsonNode ratification = answer.path("metadata").path("ratification");
        assertEquals("released_with_objections", ratification.path("state").asText());
        assertEquals(2, ratification.path("rounds").asInt());
        assertEquals(1, ratification.path("objections").size());
        assertEquals(2, publishedOfType("draft").size(), "the coordinator revises once, never a third time");
    }

    @Test
    void withOneRoundAnObjectionIsReleasedAtOnceWithoutARevision() throws Exception {
        maxRounds = 1;
        build(true);
        toRatify();
        answer(WORKLOADS, "concern", OBJECTION, 1);
        answer(METRICS, "agree", "checked", 1);
        answer(ANALYST, "agree", "checked", 1);
        settleRatification();

        JsonNode answer = theAnswer();
        assertTrue(answer.path("content").asText().contains(OBJECTION));
        assertEquals(1, publishedOfType("draft").size());
        assertTrue(revisionPrompts.isEmpty(), "no revision call");
    }

    @Test
    void aRatifierThatMissesItsDeadlineIsAFailureTheAnswerNamesNotAnAgreement() throws Exception {
        toRatify();
        answer(METRICS, "agree", "checked", 1);
        answer(ANALYST, "agree", "checked", 1);
        state().pendingEvaluations.put(WORKLOADS, 1L);
        settleRatification();

        JsonNode answer = theAnswer();
        assertTrue(answer.path("content").asText().contains("Not checked by these agents"), answer.toString());
        assertTrue(answer.path("content").asText().contains(WORKLOADS));
        JsonNode ratification = answer.path("metadata").path("ratification");
        assertEquals("not_ratified", ratification.path("state").asText());
        assertEquals(WORKLOADS, ratification.path("failures").get(0).asText());
        assertEquals(1, publishedOfType("draft").size(), "a failure is not an objection, so no revision");
        assertTrue(publishedOfType("failure").stream().anyMatch(p -> WORKLOADS.equals(p.body().path("agentName").asText())),
                "the failure is on the table");
    }

    @Test
    void aFailureSignalFromARatifierCountsAsAnAnswerAndIsNamed() throws Exception {
        toRatify();
        answer(METRICS, "agree", "checked", 1);
        answer(ANALYST, "agree", "checked", 1);
        answer(WORKLOADS, "failure", "the tool call failed", 1);
        settleRatification();

        assertEquals("not_ratified", theAnswer().path("metadata").path("ratification").path("state").asText());
    }

    @Test
    void repliesToAnotherRoundAndLateEvidenceSignalsDoNotCount() throws Exception {
        toRatify();
        send(THREAD, METRICS, "agree", "checked", "{\"ratifies\":2,\"draftId\":\"x\"}");
        send(THREAD, WORKLOADS, "agree", "late evidence, no round", null);
        send(THREAD, "some-other-agent", "agree", "not on the roster", ratifies(1));
        send(THREAD, ANALYST, "agree", "a reply to a draft from before a restart",
                "{\"ratifies\":1,\"draftId\":\"the-draft-before-the-restart\"}");
        orchestrator.checkThreadTransition(state(), java.time.Instant.now().plusSeconds(60));
        // nobody has answered round 1: the phase holds
        assertEquals(DiscussionOrchestrator.Phase.RATIFY, state().phase);
        assertTrue(publishedOfType("synthesis").isEmpty());
        assertFalse(state().agreeSignals.get(WORKLOADS).contains("late evidence"),
                "a late signal never replaces the evidence the draft rests on");
    }

    @Test
    void aStopBeforeAnyoneAnsweredReleasesTheDraftAsNotRatifiedNamingEveryAgentThatHasNotAnswered() throws Exception {
        toRatify();
        send(THREAD, "human", "stop_requested", "", null);

        JsonNode answer = theAnswer();
        JsonNode ratification = answer.path("metadata").path("ratification");
        assertEquals("not_ratified", ratification.path("state").asText(), "silence is not consent");
        assertEquals(SORTED_ROSTER, MAPPER.convertValue(ratification.path("failures"), List.class));
        assertTrue(answer.path("content").asText().contains("Not checked by these agents"), answer.toString());
    }

    @Test
    void theHardCeilingInRatifyReleasesTheDraftNotTheRawFindings() throws Exception {
        toRatify();
        answer(METRICS, "concern", OBJECTION, 1);
        state().threadCreated = state().threadCreated.minusSeconds(orchestrator.hardCeilingSeconds() + 10);

        orchestrator.checkThreadTransition(state(), java.time.Instant.now());

        JsonNode answer = theAnswer();
        assertTrue(answer.path("content").asText().startsWith(DRAFT), answer.toString());
        assertTrue(answer.path("content").asText().contains(OBJECTION));
        assertEquals("released_with_objections", answer.path("metadata").path("ratification").path("state").asText());
    }

    @Test
    void aStopWhileTheRevisionIsBeingWrittenReleasesAndTheRevisionIsNeverPublished() throws Exception {
        toRatify();
        state().ratify.beginRevision(); // the revision call is under way
        send(THREAD, "human", "stop_requested", "", null);
        theAnswer();

        assertFalse(state().ratify.revise("too late"), "the draft was released meanwhile");
        assertEquals(1, publishedOfType("draft").size());
    }

    @Test
    void aThreadFoundInRatifyAfterARestartWritesItsDraftAgainAndIgnoresRepliesToTheOldOne() throws Exception {
        toRatify();
        String firstRound = ratifies(1);

        String action = ResumeContinuation.run(orchestrator, state(), new CoordinatorThreadState());

        assertEquals(ResumeContinuation.RESTART_SYNTHESIS, action);
        await(() -> publishedOfType("draft").size() == 2, "the draft was not written again: " + timeline);
        send(THREAD, METRICS, "concern", OBJECTION, firstRound);
        assertTrue(state().ratify.objections().isEmpty(), "a reply to the draft before the restart is not a verdict");
        assertEquals(DiscussionOrchestrator.Phase.RATIFY, state().phase);
    }

    @Test
    void theRepliesAndTheDraftStayOutOfTheDiscussionTheSynthesisReads() throws Exception {
        toRatify();
        int before = state().messages.size();
        answer(METRICS, "concern", OBJECTION, 1);

        assertEquals(before, state().messages.size());
        assertTrue(state().concernSignals.isEmpty(), "a ratification concern is not evidence");
        assertEquals("14 nodes, all Ready", state().agreeSignals.get(METRICS));
    }

    @Test
    void aStopRequestInRatifyReleasesTheDraftAtOnceWithWhatIsKnown() throws Exception {
        toRatify();
        answer(METRICS, "concern", OBJECTION, 1);
        send(THREAD, "human", "stop_requested", "", null);

        JsonNode answer = theAnswer();
        assertTrue(answer.path("content").asText().startsWith(DRAFT));
        assertTrue(answer.path("content").asText().contains(OBJECTION), "the objection so far is not hidden");
        await(() -> state().phase == DiscussionOrchestrator.Phase.CLOSED, "the thread did not close");
    }

    @Test
    void ratificationTurnedOffReleasesTheSynthesisAsTheAnswerAsBefore() throws Exception {
        ratify = "false";
        build(true);
        send(THREAD, "human", "thread_start", "Are all the cluster nodes healthy?",
                "{\"crew\":\"crew\",\"namespace\":\"ns\"}");
        advanceThroughAdvisory(THREAD);
        send(THREAD, METRICS, "agree", "14 nodes", null);
        send(THREAD, WORKLOADS, "agree", "none under pressure", null);
        settle(THREAD, EVALUATION_TO_REVIEW);
        send(THREAD, ANALYST, "agree", "14 of 14", null);
        settle(THREAD, REVIEW_TO_SYNTHESIS);

        JsonNode answer = theAnswer();
        assertEquals(DRAFT, answer.path("content").asText());
        assertTrue(publishedOfType("draft").isEmpty());
        assertFalse(answer.path("metadata").has("ratification"));
    }

    @Test
    void theRosterIsTheContributorsOfTheTurn() throws Exception {
        toRatify();
        var viaState = orchestrator.ratifyRoster(state());
        assertEquals(SORTED_ROSTER, List.copyOf(viaState));
    }
}
