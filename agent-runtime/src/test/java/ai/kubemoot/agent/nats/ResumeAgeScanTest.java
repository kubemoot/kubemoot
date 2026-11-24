package ai.kubemoot.agent.nats;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTimeoutPreemptively;

import java.time.Duration;
import java.util.Optional;
import org.junit.jupiter.api.Test;

class ResumeAgeScanTest {

    @Test
    void twoLetterUnitsAreReadBeforeSingleLetters() {
        assertEquals(Optional.of(Duration.ofMillis(1)), ResumeAge.parse("1ms"));
        assertEquals(Optional.of(Duration.ofNanos(1_000)), ResumeAge.parse("1us"));
        assertEquals(Optional.of(Duration.ofNanos(7)), ResumeAge.parse("7ns"));
        assertEquals(Optional.of(Duration.ofSeconds(65)), ResumeAge.parse("1m5s"));
    }

    @Test
    void partsMustFollowEachOtherWithNothingBetween() {
        assertEquals(Optional.empty(), ResumeAge.parse("1m 5s"));
        assertEquals(Optional.empty(), ResumeAge.parse("1mx5s"));
        assertEquals(Optional.empty(), ResumeAge.parse("m5"));
    }

    @Test
    void numbersNeedDigitsOnBothSidesOfADot() {
        assertEquals(Optional.empty(), ResumeAge.parse("1.s"));
        assertEquals(Optional.empty(), ResumeAge.parse(".5s"));
        assertEquals(Optional.empty(), ResumeAge.parse("1..5s"));
    }

    @Test
    void aNumberWithoutAUnitIsNotADuration() {
        assertEquals(Optional.empty(), ResumeAge.parse("5"));
        assertEquals(Optional.empty(), ResumeAge.parse("5d"));
    }

    @Test
    void aLongInvalidValueIsRejectedQuickly() {
        String hostile = "1".repeat(200_000) + "x";
        assertTimeoutPreemptively(Duration.ofSeconds(2), () -> assertEquals(Optional.empty(), ResumeAge.parse(hostile)));
    }
}
