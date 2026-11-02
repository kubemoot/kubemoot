package ai.kubemoot.agent.nats;

import java.util.List;
import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.*;

class EvidenceReportTest {

    @Test
    void theLogLineHasEveryFigure() {
        var report = new EvidenceReport(5);
        report.roundStarted(8192, 3000);
        report.roundStarted(32768, 20000);
        report.delivered(100, false);
        report.delivered(50, true);
        report.finish(4, List.of("k5"));

        assertEquals("synthesis evidence: thread=t1 windows=[8192, 32768] rooms=[3000, 20000] results=5 filled=4"
                + " rounds=2 chunks=1 bytes=150 notDelivered=[k5]", report.logLine("t1"));
        assertEquals(2, report.rounds());
        assertEquals(150, report.filledBytes());
        assertEquals(List.of("k5"), report.notDelivered());
    }

    @Test
    void anUnknownWindowAndRoomShowAsUnknown() {
        var report = new EvidenceReport(1);
        report.roundStarted(EvidenceReport.UNKNOWN, EvidenceReport.UNKNOWN);
        report.finish(1, List.of());

        assertTrue(report.logLine("t").contains("windows=[unknown] rooms=[unknown]"), report.logLine("t"));
    }

    @Test
    void aReportWithNothingDeliveredIsAllZeros() {
        var report = new EvidenceReport(0);

        assertEquals(0, report.rounds());
        assertEquals(0, report.filledBytes());
        assertEquals(0, report.chunks());
        assertTrue(report.logLine("t").contains("results=0 filled=0 rounds=0 chunks=0 bytes=0 notDelivered=[]"));
    }
}
