package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class CrewDataGatherersTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();

    @Test
    void gatherersAreTheToolersAndTheAgentsThatDeclareNoRoleInResumeOrder() throws Exception {
        var resumes = MAPPER.readTree("""
                [{"name":"nodes","role":"tooler"},{"name":"compute","role":"analyst"},
                 {"name":"search","role":"researcher"},{"name":"lead","role":"coordinator"},
                 {"name":"plain"},{"name":"a-skill","kind":"skill"},{"role":"tooler"},{"name":"","role":"tooler"},
                 {"name":"workloads","role":"tooler"}]""");

        assertEquals(List.of("nodes", "plain", "workloads"), CrewDataGatherers.gatherers(resumes));
    }

    @Test
    void whatIsNotAListOfResumesHasNoGatherers() throws Exception {
        assertTrue(CrewDataGatherers.gatherers(null).isEmpty());
        assertTrue(CrewDataGatherers.gatherers(MAPPER.readTree("{\"name\":\"x\"}")).isEmpty());
        assertTrue(CrewDataGatherers.gatherers(MAPPER.readTree("[]")).isEmpty());
    }

    @Test
    void aSubcommitteeIsAnalystsOnlyWhenItHasMembersAndEveryOneIsAnAnalyst() {
        var analysts = Set.of("compute", "advisor");

        assertTrue(CrewDataGatherers.analystsOnly(Set.of("compute"), analysts));
        assertTrue(CrewDataGatherers.analystsOnly(Set.of("compute", "advisor"), analysts));
        assertFalse(CrewDataGatherers.analystsOnly(Set.of("compute", "nodes"), analysts), "mixed");
        assertFalse(CrewDataGatherers.analystsOnly(Set.of("nodes"), analysts));
        assertFalse(CrewDataGatherers.analystsOnly(Set.of(), analysts), "nobody was selected");
        assertFalse(CrewDataGatherers.analystsOnly(null, analysts));
        assertFalse(CrewDataGatherers.analystsOnly(Set.of("compute"), Set.of()), "a crew without analysts");
        assertFalse(CrewDataGatherers.analystsOnly(Set.of("compute"), null));
    }

    @Test
    void theBestRankedGatherersAreChosenAndAnalystsInTheRankingAreSkipped() {
        var ranked = List.of("compute", "workloads", "advisor", "nodes", "storage");
        var gatherers = List.of("nodes", "storage", "workloads");

        assertEquals(List.of("workloads", "nodes"), CrewDataGatherers.choose(ranked, gatherers, 2));
    }

    @Test
    void gatherersTheRankingMissedFollowInResumeOrderAndWithoutARankingResumeOrderDecides() {
        var gatherers = List.of("nodes", "storage", "workloads");

        assertEquals(List.of("storage", "nodes"), CrewDataGatherers.choose(List.of("storage"), gatherers, 2));
        assertEquals(List.of("nodes", "storage"), CrewDataGatherers.choose(null, gatherers, 2));
        assertEquals(List.of("nodes", "storage"), CrewDataGatherers.choose(List.of(), gatherers, 2));
    }

    @Test
    void fewerGatherersThanTheLimitAreAllChosenAndNoneChoosesNone() {
        assertEquals(List.of("nodes"), CrewDataGatherers.choose(List.of("nodes"), List.of("nodes"), 2));
        assertTrue(CrewDataGatherers.choose(List.of("nodes"), List.of(), 2).isEmpty());
    }
}
