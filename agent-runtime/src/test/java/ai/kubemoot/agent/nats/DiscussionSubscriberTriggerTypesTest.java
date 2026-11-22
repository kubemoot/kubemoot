package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/**
 * Unit tests for {@link DiscussionSubscriber#triggerTypesFor(String)} — the
 * role-based selection of which phase messages an agent acts on.
 *
 * Callers/toolers act in EVALUATING (advisory_ready) + multi-round turns;
 * analysts act in REVIEW (review_ready) only. review_ready is a trigger ONLY
 * for analysts, so re-enabling it cannot revive the all-agents re-triage storm.
 */
class DiscussionSubscriberTriggerTypesTest {

    @Test
    void analystTriggersOnReviewReadyAndTheDraftOnly() {
        var t = DiscussionSubscriber.triggerTypesFor("analyst");
        assertTrue(t.contains("review_ready"), "analyst must trigger on review_ready");
        assertFalse(t.contains("advisory_ready"), "analyst must NOT trigger on advisory_ready");
        assertTrue(t.contains("draft"), "analyst ratifies the coordinator's draft");
        assertEquals(2, t.size(), "analyst triggers on review_ready and the draft only");
    }

    @Test
    void analystRoleIsCaseInsensitive() {
        assertTrue(DiscussionSubscriber.triggerTypesFor("Analyst").contains("review_ready"));
        assertTrue(DiscussionSubscriber.triggerTypesFor("ANALYST").contains("review_ready"));
    }

    @Test
    void toolerTriggersOnEvaluatingNotReview() {
        var t = DiscussionSubscriber.triggerTypesFor("tooler");
        assertTrue(t.contains("advisory_ready"), "tooler must trigger on advisory_ready");
        assertTrue(t.contains("follow_up"));
        assertTrue(t.contains("reply"));
        assertTrue(t.contains("draft"), "tooler ratifies the coordinator's draft");
        assertFalse(t.contains("review_ready"), "tooler must NOT trigger on review_ready");
    }

    @Test
    void unknownOrNullRoleDefaultsToToolerTriggers() {
        for (String role : new String[]{"coordinator", "researcher", "observer", "", null}) {
            var t = DiscussionSubscriber.triggerTypesFor(role);
            assertTrue(t.contains("advisory_ready"), "role '" + role + "' defaults to tooler triggers");
            assertFalse(t.contains("review_ready"), "role '" + role + "' must not trigger on review_ready");
        }
    }
}
