package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

/** The characters-per-token ratio is learned from the engine's reported prompt sizes. */
class CharsPerTokenTest {

    @Test
    void beforeAnyReport_theDefaultApplies() {
        var r = new CharsPerToken();
        assertEquals(CharsPerToken.DEFAULT, r.ratio());
        // The estimate never goes under the dense-text ratio, even before any report.
        assertEquals(1_167, r.tokens(3_500));
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

    @Test
    void aMeasuredRatioAboveTheDenseRatio_isNotTrustedForTheEstimate() {
        var r = new CharsPerToken();
        r.observe(70_000, 10_000); // prose-heavy prompt: 7 characters per token
        assertEquals(7.0, r.ratio(), 1e-9);
        assertEquals(CharsPerToken.DENSE, r.estimateRatio(), 1e-9);
        assertEquals(1_000, r.tokens(3_000));
    }

    @Test
    void aMeasuredRatioBelowTheDenseRatio_raisesTheEstimate() {
        var r = new CharsPerToken();
        r.observe(25_000, 10_000); // JSON-heavy prompt: 2.5 characters per token, as the engine counted it
        assertEquals(2.5, r.estimateRatio(), 1e-9);
        assertEquals(1_200, r.tokens(3_000));
    }

    @Test
    void theEstimateIsNeverLowerThanTheCountAtEitherRatio() {
        for (double measured : new double[] {1.5, 2.5, 3.0, 3.5, 6.0}) {
            var r = new CharsPerToken();
            r.observe((long) (measured * 1_000), 1_000);
            long chars = 12_345;
            long atMeasured = (long) Math.ceil(chars / measured);
            long atDense = (long) Math.ceil(chars / CharsPerToken.DENSE);
            assertTrue(r.tokens(chars) >= Math.max(atMeasured, atDense) - 1, "measured " + measured);
        }
    }
}
