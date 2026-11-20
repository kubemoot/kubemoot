package ai.kubemoot.agent.requester;

import org.junit.jupiter.api.Test;

import java.time.Instant;

import static org.junit.jupiter.api.Assertions.assertEquals;

class TurnContextTest {

    private static final Instant ARRIVED = Instant.parse("2026-11-20T15:00:00Z");

    @Test
    void theLineNamesTheDateTheZoneTheNamespaceAndTheCrew() {
        var requester = Requester.arrived(Requester.of("America/New_York", null).orElseThrow(), null, ARRIVED);
        assertEquals("Today is 2026-11-20 (America/New_York). You are in namespace crew-homelab-pilot, crew homelab-pilot.",
                TurnContext.line(requester, "crew-homelab-pilot", "homelab-pilot"));
    }

    @Test
    void theLineAlwaysNamesTheZoneEvenWhenTheClientSentNone() {
        var requester = Requester.arrived(null, null, ARRIVED);
        assertEquals("Today is 2026-11-20 (UTC). You are in namespace ns, crew c.",
                TurnContext.line(requester, "ns", "c"));
    }

    @Test
    void theLineCarriesNoClockTime() {
        var requester = Requester.arrived(null, "Asia/Tokyo", ARRIVED);
        String line = TurnContext.line(requester, "ns", "c");
        assertEquals("Today is 2026-11-21 (Asia/Tokyo). You are in namespace ns, crew c.", line);
    }

    @Test
    void aPartThatIsNotKnownIsLeftOut() {
        var requester = Requester.arrived(null, null, ARRIVED);
        assertEquals("Today is 2026-11-20 (UTC).", TurnContext.line(requester, null, null));
        assertEquals("You are in namespace ns.", TurnContext.line(null, "ns", " "));
        assertEquals("You are in crew c.", TurnContext.line(null, null, "c"));
        assertEquals("", TurnContext.line(null, null, null));
    }

    @Test
    void appendToKeepsThePromptFirst() {
        assertEquals("PROMPT\n\nLINE", TurnContext.appendTo("PROMPT", "LINE"));
        assertEquals("LINE", TurnContext.appendTo(null, "LINE"));
        assertEquals("PROMPT", TurnContext.appendTo("PROMPT", ""));
        assertEquals("PROMPT", TurnContext.appendTo("PROMPT", null));
    }
}
