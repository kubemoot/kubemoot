package ai.kubemoot.agent.nats;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.List;
import org.junit.jupiter.api.Test;

class NumberGroundingTest {

    private static final String RESULT = "total=100 GiB used=53.333 pct=33.333333 count=1,234 pods=7";

    @Test
    void figuresAreNumbersOfTwoDigitsOrADecimalStandingAlone() {
        assertEquals(List.of("50", "100", "33.3", "1,234"),
                NumberGrounding.figuresIn("Requested 50 GiB of 100GiB (33.3%), 1,234 pods in 5 nodes."));
    }

    @Test
    void namesVersionsAndPathsAreNotFigures() {
        assertTrue(NumberGrounding.figuresIn("homelab-k8s-1-cp k8s v1.37.1 /kubemoot/workspace/003-compute 7").isEmpty());
        assertTrue(NumberGrounding.figuresIn("").isEmpty());
        assertTrue(NumberGrounding.figuresIn(null).isEmpty());
    }

    @Test
    void aFigureARoundedResultHoldsIsGrounded() {
        assertTrue(NumberGrounding.ungrounded("100 GiB, 53.3 used, 33.33%, 1234 pods", List.of(RESULT)).isEmpty());
        assertTrue(NumberGrounding.ungrounded("about 100", List.of("99.6")).isEmpty());
    }

    @Test
    void aFigureNoSourceHoldsIsListedInOrder() {
        assertEquals(List.of("50", "12.5"),
                NumberGrounding.ungrounded("100 GiB capacity, 50 requested, 12.5 percent", List.of(RESULT)));
    }

    @Test
    void aFigureOfAnyOneSourceIsEnough() {
        assertTrue(NumberGrounding.ungrounded("42 and 100", java.util.Arrays.asList("the question says 42", RESULT, null)).isEmpty());
    }

    @Test
    void aFigureMoreThanHalfAUnitAwayIsNotGrounded() {
        assertEquals(List.of("34.0"), NumberGrounding.ungrounded("34.0", List.of("33.4 and 33.0")));
        assertEquals(List.of("98"), NumberGrounding.ungrounded("98", List.of("100 and 97.4")));
    }

    @Test
    void theRepromptAndTheWithheldAnswerNameTheFigures() {
        assertTrue(NumberGrounding.reprompt(List.of("50", "100")).contains("50, 100"));
        String withheld = NumberGrounding.withheld(List.of("50"));
        assertTrue(withheld.startsWith("NO_DATA: could not compute"));
        assertTrue(withheld.contains("(50)"));
    }

    @Test
    void figuresInCompactJsonRangesAndNegativesGroundAnAnswer() {
        assertTrue(NumberGrounding.ungrounded("100 GiB, 12.5 lower, 15 and 20",
                List.of("{\"total\":100,\"delta\":-12.5,\"range\":\"15-20\"}")).isEmpty());
    }

    @Test
    void aListMarkerIsNotAFigure() {
        assertEquals(List.of("40"), NumberGrounding.figuresIn("10. first\n12) second 40 items"));
    }
}
