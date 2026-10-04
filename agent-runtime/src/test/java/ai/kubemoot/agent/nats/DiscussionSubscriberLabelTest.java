package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

/** Tests for the DiscussionSubscriber thread-label helper. Plain JUnit 5. */
class DiscussionSubscriberLabelTest {

    @Test
    void fixedLabelsIgnoreTheAuthor() {
        assertEquals("User Question", DiscussionSubscriber.threadLabel("thread_start", "human"));
        assertEquals("Evaluation Phase Started", DiscussionSubscriber.threadLabel("advisory_ready", "coord"));
        assertEquals("Review Phase - Other Agents' Responses", DiscussionSubscriber.threadLabel("review_ready", "c"));
        assertEquals("User Reply", DiscussionSubscriber.threadLabel("reply", "human"));
        assertEquals("Facilitator Follow-up", DiscussionSubscriber.threadLabel("follow_up", "coord"));
        assertEquals("Synthesis", DiscussionSubscriber.threadLabel("synthesis", "coord"));
    }

    @Test
    void authoredLabelsNameTheAgent() {
        assertEquals("Advisory (a)", DiscussionSubscriber.threadLabel("advisory", "a"));
        assertEquals("Response (a)", DiscussionSubscriber.threadLabel("agree", "a"));
        assertEquals("Response (a)", DiscussionSubscriber.threadLabel("contribution", "a"));
        assertEquals("Concern (a)", DiscussionSubscriber.threadLabel("concern", "a"));
        assertEquals("Stand Aside (a)", DiscussionSubscriber.threadLabel("stand_aside", "a"));
        assertEquals("Stand Aside (a)", DiscussionSubscriber.threadLabel("decline", "a"));
        assertEquals("Proposal (a)", DiscussionSubscriber.threadLabel("proposal", "a"));
    }

    @Test
    void unknownTypeShowsTheRawTypeAndAuthor() {
        assertEquals("heartbeat (a)", DiscussionSubscriber.threadLabel("heartbeat", "a"));
        assertEquals(" (a)", DiscussionSubscriber.threadLabel("", "a"));
    }
}
