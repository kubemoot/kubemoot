package ai.kubemoot.agent.nats;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.*;

class ReviewRosterTest {

    private static final Set<String> ALL = Set.of("compute", "k8s-advisor", "obs-advisor", "proxmox-advisor");
    private static final List<String> RANKING =
            List.of("k8s-config", "k8s-advisor", "obs-advisor", "compute", "proxmox-advisor");

    @Test
    void concurrer_isTheBestRankedSelectedAnalyst() {
        assertEquals(List.of("k8s-advisor"),
                ReviewRoster.concurrer(List.of("compute", "k8s-advisor"), ALL, RANKING, Set.of()));
    }

    @Test
    void concurrer_amongUnrankedSelectedAnalysts_isTheFirstByName() {
        assertEquals(List.of("compute"),
                ReviewRoster.concurrer(List.of("obs-advisor", "compute"), ALL, List.of(), Set.of()));
        assertEquals(List.of("compute"),
                ReviewRoster.concurrer(List.of("obs-advisor", "compute"), ALL, null, Set.of()));
    }

    @Test
    void concurrer_withNoneSelected_isTheBestMatchAmongAllAnalysts() {
        assertEquals(List.of("k8s-advisor"), ReviewRoster.concurrer(List.of(), ALL, RANKING, Set.of()));
    }

    @Test
    void concurrer_withNoneSelectedAndNoRanking_isNobody_neverEveryAnalyst() {
        assertEquals(List.of(), ReviewRoster.concurrer(List.of(), ALL, List.of(), Set.of()));
        assertEquals(List.of(), ReviewRoster.concurrer(null, ALL, null, null));
    }

    @Test
    void concurrer_skipsTheExcludedAnalyst() {
        assertEquals(List.of("obs-advisor"),
                ReviewRoster.concurrer(List.of(), ALL, RANKING, Set.of("k8s-advisor")));
    }

    @Test
    void full_isEverySelectedAnalyst_inNameOrder() {
        assertEquals(List.of("compute", "k8s-advisor"),
                ReviewRoster.full(List.of("k8s-advisor", "compute"), ALL, RANKING, Set.of()));
    }

    @Test
    void full_withNoneSelected_isOnlyTheBestMatch() {
        assertEquals(List.of("k8s-advisor"), ReviewRoster.full(List.of(), ALL, RANKING, Set.of()));
    }

    @Test
    void full_afterAConcern_excludesTheConcurrer_andFallsBackToTheNextBestMatch() {
        assertEquals(List.of("compute"),
                ReviewRoster.full(List.of("compute", "k8s-advisor"), ALL, RANKING, Set.of("k8s-advisor")));
        assertEquals(List.of("obs-advisor"),
                ReviewRoster.full(List.of("k8s-advisor"), ALL, RANKING, Set.of("k8s-advisor")),
                "when the concurrer was the only selected analyst, the next best match reviews");
    }

    @Test
    void rankingNamesThatAreNotAnalysts_areIgnored() {
        assertEquals(List.of(), ReviewRoster.concurrer(List.of(), ALL, List.of("k8s-config", "k8s-helm"), Set.of()));
    }
}
