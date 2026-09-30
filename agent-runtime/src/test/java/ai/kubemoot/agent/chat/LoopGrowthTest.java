package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class LoopGrowthTest {

    @Test
    void beforeAnyLoop_theCallersDefaultApplies() {
        assertEquals(4096, new LoopGrowth().estimate(4096));
        assertEquals(0, new LoopGrowth().estimate(-5), "a negative default is no growth");
    }

    @Test
    void estimate_isTheLargestRecentGrowth() {
        var growth = new LoopGrowth();
        growth.record(5_000, 6_000);
        growth.record(5_000, 9_500);
        growth.record(4_000, 4_200);
        assertEquals(4_500, growth.estimate(4096));
    }

    @Test
    void aLoopThatNeverGrew_recordsZero_andReplacesTheDefault() {
        var growth = new LoopGrowth();
        growth.record(5_000, 5_000);
        assertEquals(0, growth.estimate(4096));
    }

    @Test
    void unknownSizes_areIgnored() {
        var growth = new LoopGrowth();
        growth.record(0, 9_000);
        growth.record(5_000, 0);
        growth.record(-1, -1);
        assertEquals(4096, growth.estimate(4096), "nothing measurable was recorded");
    }

    @Test
    void peakBelowTheFirstPrompt_countsAsNoGrowth() {
        var growth = new LoopGrowth();
        growth.record(5_000, 3_000);
        assertEquals(0, growth.estimate(4096));
    }

    @Test
    void onlyTheMostRecentWindowCounts() {
        var growth = new LoopGrowth();
        growth.record(1_000, 21_000);
        for (int i = 0; i < LoopGrowth.WINDOW; i++) {
            growth.record(1_000, 2_000);
        }
        assertEquals(1_000, growth.estimate(4096), "a large loop older than the window no longer counts");
    }
}
