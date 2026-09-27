package ai.kubemoot.agent.provider;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.*;

/** Queue for a loaded copy, or unload idle models: the decision and its inputs. */
class PlacementCostModelTest {

    private static final SwitchCostProfile OLLAMA = OllamaDriver.PROFILE;
    /** A cheaper switch: releasing is nearly free and a released model comes back ten times faster. */
    private static final SwitchCostProfile CHEAP_RELEASE = new SwitchCostProfile(1024.0, 0.2, 10240.0);
    private static final long QWEN32_MIB = 27_000;
    private static final long QWEN14_MIB = 12_224;
    private static final long QWEN8_MIB = 6_000;

    private static PlacementCostModel.EvictionPlan plan(SwitchCostProfile profile, long requested,
                                                        PlacementCostModel.Victim... victims) {
        return new PlacementCostModel.EvictionPlan("gpu", List.of(victims),
                PlacementCostModel.evictionCostSeconds(profile, requested, List.of(victims)));
    }

    @Test
    void bigWarmModelIsNotUnloadedForAModelAboutToFreeUpElsewhere() {
        // 5090: qwen3:32b warm, modest recent use. 4090: qwen3:14b busy with one call
        // that started 5 seconds ago; calls there take about 20 seconds.
        double wait = OllamaDriver.slotWaitSeconds(List.of(5.0), 1, 20.0);
        var queue = new PlacementCostModel.QueueOption("qwen3:14b", "4090", wait);
        var evict32 = plan(OLLAMA, QWEN14_MIB,
                new PlacementCostModel.Victim("qwen3:32b", QWEN32_MIB, new ModelDemand(0, 0, 1.0, 0.0, 1L)));

        var decision = PlacementCostModel.decide(Optional.of(queue), Optional.of(evict32));

        assertEquals(15.0, wait, 0.001);
        assertEquals(PlacementCostModel.Action.QUEUE, decision.action());
        assertTrue(evict32.costSeconds() > wait, "unloading a wanted 32b costs more than a short wait");
    }

    @Test
    void deepQueueAndAnUnwantedSmallModel_evictionWins() {
        // Five calls queued on one slot of the only qwen3:14b copy; an idle, unwanted qwen3:8b elsewhere.
        double wait = OllamaDriver.slotWaitSeconds(List.of(5.0, 3.0, 2.0, 1.0, 0.5), 1, 20.0);
        var queue = new PlacementCostModel.QueueOption("qwen3:14b", "4090", wait);
        var evict8 = plan(OLLAMA, QWEN14_MIB, new PlacementCostModel.Victim("qwen3:8b", QWEN8_MIB, ModelDemand.NONE));

        var decision = PlacementCostModel.decide(Optional.of(queue), Optional.of(evict8));

        assertEquals(95.0, wait, 0.001);
        assertEquals(PlacementCostModel.Action.EVICT, decision.action());
        assertEquals(List.of("qwen3:8b"), decision.eviction().orElseThrow().victimModels());
    }

    @Test
    void cheapReleaseAndFastWake_turnTheSameSituationIntoAnEviction() {
        double wait = OllamaDriver.slotWaitSeconds(List.of(5.0), 1, 20.0);
        var queue = Optional.of(new PlacementCostModel.QueueOption("qwen3:14b", "4090", wait));
        var victim = new PlacementCostModel.Victim("qwen3:32b", QWEN32_MIB, new ModelDemand(0, 0, 1.0, 0.0, 1L));

        assertEquals(PlacementCostModel.Action.QUEUE,
                PlacementCostModel.decide(queue, Optional.of(plan(OLLAMA, QWEN14_MIB, victim))).action());
        assertEquals(PlacementCostModel.Action.EVICT,
                PlacementCostModel.decide(queue, Optional.of(plan(CHEAP_RELEASE, QWEN14_MIB, victim))).action(),
                "when release is cheap and wake is fast, the wait is the costlier option");
    }

    @Test
    void neitherOption_waitsForMemory() {
        assertEquals(PlacementCostModel.Action.WAIT_FOR_MEMORY,
                PlacementCostModel.decide(Optional.empty(), Optional.empty()).action());
    }

    @Test
    void onlyOneOption_takesIt() {
        var queue = new PlacementCostModel.QueueOption("m", "p", 500.0);
        assertEquals(PlacementCostModel.Action.QUEUE, PlacementCostModel.decide(Optional.of(queue), Optional.empty()).action());
        var evict = plan(OLLAMA, QWEN14_MIB, new PlacementCostModel.Victim("x", 1_000, ModelDemand.NONE));
        assertEquals(PlacementCostModel.Action.EVICT, PlacementCostModel.decide(Optional.empty(), Optional.of(evict)).action());
    }

    @Test
    void evictionCost_growsWithVictimSizeAndDemand() {
        var small = new PlacementCostModel.Victim("s", 6_000, new ModelDemand(0, 0, 2.0, 0.0, 0L));
        var large = new PlacementCostModel.Victim("l", 27_000, new ModelDemand(0, 0, 2.0, 0.0, 0L));
        var unwanted = new PlacementCostModel.Victim("u", 27_000, ModelDemand.NONE);
        double base = OLLAMA.coldLoadSeconds(QWEN14_MIB);
        assertTrue(PlacementCostModel.evictionCostSeconds(OLLAMA, QWEN14_MIB, List.of(large))
                > PlacementCostModel.evictionCostSeconds(OLLAMA, QWEN14_MIB, List.of(small)));
        assertEquals(base + OLLAMA.releaseSeconds(),
                PlacementCostModel.evictionCostSeconds(OLLAMA, QWEN14_MIB, List.of(unwanted)), 0.001);
    }

    @Test
    void slotWait_isZeroWithAFreeSlot_andOverdueCallsCountAsFinishing() {
        assertEquals(0.0, OllamaDriver.slotWaitSeconds(List.of(), 1, 20.0));
        assertEquals(0.0, OllamaDriver.slotWaitSeconds(List.of(5.0), 2, 20.0));
        assertEquals(0.0, OllamaDriver.slotWaitSeconds(List.of(40.0), 1, 20.0));
        assertEquals(20.0, OllamaDriver.slotWaitSeconds(List.of(10.0, 10.0, 0.0), 2, 20.0), 0.001);
    }
}
