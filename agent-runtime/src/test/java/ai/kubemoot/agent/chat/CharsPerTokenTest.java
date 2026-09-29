package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/** The characters-per-token ratio is learned from the engine's reported prompt sizes. */
class CharsPerTokenTest {

    @Test
    void beforeAnyReport_theDefaultApplies() {
        var r = new CharsPerToken();
        assertEquals(CharsPerToken.DEFAULT, r.ratio());
        assertEquals(1_000, r.tokens(3_500));
        assertEquals(0, r.tokens(0));
        assertEquals(0, r.tokens(-5));
    }

    @Test
    void theFirstReportSetsTheRatio_laterReportsMoveItGradually() {
        var r = new CharsPerToken();
        r.observe(30_000, 10_000);
        assertEquals(3.0, r.ratio(), 1e-9);
        r.observe(40_000, 10_000);
        assertEquals(3.0 + CharsPerToken.ALPHA * (4.0 - 3.0), r.ratio(), 1e-9);
    }

    @Test
    void implausibleOrEmptyReports_areIgnored() {
        var r = new CharsPerToken();
        r.observe(100, 1_000);      // 0.1 chars per token
        r.observe(1_000_000, 10);   // 100,000 chars per token
        r.observe(0, 10);
        r.observe(10, 0);
        assertEquals(CharsPerToken.DEFAULT, r.ratio());
    }
}
