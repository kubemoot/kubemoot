package ai.kubemoot.agent.modelserver;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

/** The tokens an agent's calls have been generating, per model and call kind. */
class OutputLengthsTest {

    private static OutputLengths with(String model, String kind, long... tokens) {
        var lengths = new OutputLengths();
        for (long t : tokens) {
            lengths.add(model, kind, t);
        }
        return lengths;
    }

    @Test
    void isTheMedianOfTheRecentCalls() {
        assertEquals(500, with("m", "simple", 900, 500, 100).expected("m", "simple"));
        assertEquals(450, with("m", "simple", 900, 500, 400, 100).expected("m", "simple"), "even counts take the middle two");
    }

    @Test
    void saysNothingWithTooFewCalls_orForAModelOrKindItHasNotSeen() {
        var lengths = with("m", "simple", 500, 500);
        assertEquals(0, lengths.expected("m", "simple"));
        assertEquals(0, lengths.expected("other", "simple"));
        assertEquals(0, with("m", "simple", 500, 500, 500).expected("m", "triage"));
    }

    @Test
    void keepsModelsAndKindsApart() {
        var lengths = with("m", "triage", 250, 250, 250);
        for (long t : new long[] {900, 900, 900}) {
            lengths.add("m", "tool-turn", t);
            lengths.add("n", "triage", t * 2);
        }
        assertEquals(250, lengths.expected("m", "triage"));
        assertEquals(900, lengths.expected("m", "tool-turn"));
        assertEquals(1_800, lengths.expected("n", "triage"));
    }

    @Test
    void followsTheLastSixteenCalls() {
        var lengths = new OutputLengths();
        for (int i = 0; i < OutputLengths.WINDOW; i++) {
            lengths.add("m", "k", 100);
        }
        for (int i = 0; i < OutputLengths.WINDOW; i++) {
            lengths.add("m", "k", 700);
        }
        assertEquals(700, lengths.expected("m", "k"), "the older calls fell out of the window");
    }

    @Test
    void ignoresCallsThatGeneratedNothingOrNameNoModel() {
        var lengths = new OutputLengths();
        lengths.add("m", "k", 0);
        lengths.add("m", "k", -5);
        lengths.add(null, "k", 100);
        lengths.add("m", null, 100);
        for (int i = 0; i < OutputLengths.MIN_CALLS; i++) {
            lengths.add("m", "k", 40);
        }
        assertEquals(40, lengths.expected("m", "k"));
    }

    @Test
    void olderCallsGoBehindNewerOnes_andNeverPushThemOut() {
        var lengths = new OutputLengths();
        for (int i = 0; i < OutputLengths.WINDOW - 1; i++) {
            lengths.add("m", "k", 700);
        }
        lengths.addOlder("m", "k", 100);
        lengths.addOlder("m", "k", 100);
        lengths.addOlder("m", "k", 100);

        assertEquals(700, lengths.expected("m", "k"), "only one older call fitted, and it is behind the newest");
        var small = new OutputLengths();
        small.add("m", "k", 700);
        small.addOlder("m", "k", 100);
        small.addOlder("m", "k", 100);
        assertEquals(100, small.expected("m", "k"), "older history fills a window that the new calls leave empty");
        small.addOlder("m", "k", 0);
        small.addOlder(null, "k", 5);
        assertEquals(100, small.expected("m", "k"));
    }
}
