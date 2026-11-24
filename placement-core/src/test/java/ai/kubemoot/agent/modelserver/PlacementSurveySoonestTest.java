package ai.kubemoot.agent.modelserver;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import ai.kubemoot.agent.modelserver.LearnedPlacement.Prediction;
import ai.kubemoot.agent.modelserver.PlacementSurvey.Surveyed;
import ai.kubemoot.agent.modelserver.PlacementSurvey.Survey;
import java.util.List;
import java.util.Optional;
import org.junit.jupiter.api.Test;

class PlacementSurveySoonestTest {

    private static Surveyed predicted(double seconds) {
        return new Surveyed("m", null, Optional.of(new Prediction(0, 0, 0, seconds)));
    }

    private static Surveyed unmeasured() {
        return new Surveyed("m", null, Optional.empty());
    }

    @Test
    void theOptionWithTheLowestTotalIsChosen() {
        var fast = predicted(1);
        var survey = new Survey("m", List.of(predicted(5), fast, predicted(3)));
        assertEquals(fast, survey.soonest().orElseThrow());
    }

    @Test
    void aTieKeepsTheFirstOption() {
        var first = predicted(2);
        var survey = new Survey("m", List.of(first, predicted(2)));
        assertTrue(survey.soonest().orElseThrow() == first);
    }

    @Test
    void anyUnmeasuredOptionMeansNoChoice() {
        assertTrue(new Survey("m", List.of(predicted(1), unmeasured())).soonest().isEmpty());
    }

    @Test
    void noOptionsMeansNoChoice() {
        assertTrue(new Survey("m", List.of()).soonest().isEmpty());
    }
}
