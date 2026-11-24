package ai.kubemoot.agent.nats;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Optional;
import java.util.Set;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ReviewConveningTest {

    private static final ObjectMapper MAPPER = new ObjectMapper();
    private static final String PROXMOX_PVE = "proxmox-pve";
    private static final String PROXMOX_ADVISOR = "proxmox-advisor";
    private static final String GPU_ADVISOR = "nvidia-gpu-advisor";
    private static final String K8S_ADVISOR = "k8s-advisor";
    private static final String K8S_NODES = "k8s-nodes";

    private static final ReviewConvening.Policy DOMAIN_PRIMARY =
            new ReviewConvening.Policy(ReviewConvening.Convene.DOMAIN, ReviewConvening.Fallback.PRIMARY);
    private static final ReviewConvening.Policy DOMAIN_ALL =
            new ReviewConvening.Policy(ReviewConvening.Convene.DOMAIN, ReviewConvening.Fallback.ALL);

    private static final String CREW = """
            [{"name":"proxmox-pve","role":"tooler","domains":["proxmox","hypervisor"]},
             {"name":"k8s-nodes","role":"tooler","domains":["kubernetes"]},
             {"name":"internet-search","role":"tooler","keywords":["proxmox","gpu","kubernetes"]},
             {"name":"proxmox-advisor","role":"analyst","domains":["proxmox","virtualization"]},
             {"name":"nvidia-gpu-advisor","role":"analyst","domains":["gpu","cuda"]},
             {"name":"k8s-advisor","role":"analyst","domains":["kubernetes"]},
             {"name":"obs-advisor","role":"analyst","domains":["prometheus","metrics"]},
             {"name":"a-skill","kind":"skill","description":"x"}]
            """;

    private static JsonNode crew() throws Exception {
        return MAPPER.readTree(CREW);
    }

    private static ReviewConvening.Plan plan(ReviewConvening.Policy policy, List<String> domains,
                                             List<String> gatherers, List<String> selected, String query)
            throws Exception {
        return ReviewConvening.plan(policy, crew(), domains, gatherers, selected, query);
    }

    @Test
    void domainMode_aProxmoxAnswerIsReviewedByTheProxmoxAnalystOnly() throws Exception {
        var plan = plan(DOMAIN_PRIMARY, List.of("Proxmox VE"), List.of(PROXMOX_PVE), List.of(), "Describe the proxmox nodes.");

        assertEquals(List.of(PROXMOX_ADVISOR), plan.circle());
        assertEquals(ReviewConvening.BASIS_DOMAIN, plan.basis());
    }

    @Test
    void domainMode_theGatheringToolersDomainsCountEvenWhenTheQuestionNamesNone() throws Exception {
        var plan = plan(DOMAIN_PRIMARY, List.of(), List.of(K8S_NODES), List.of(), "How many namespaces?");

        assertEquals(List.of(K8S_ADVISOR), plan.circle());
    }

    @Test
    void domainMode_aQuestionThatSpansDomainsConvenesEachAnalystItTouches() throws Exception {
        var plan = plan(DOMAIN_PRIMARY, List.of("kubernetes", "GPU"), List.of(K8S_NODES), List.of(), "q");

        assertEquals(Set.of(K8S_ADVISOR, GPU_ADVISOR), Set.copyOf(plan.circle()));
    }

    @Test
    void domainMode_keywordsNeverMatch_onlyDeclaredDomainsDo() throws Exception {
        // internet-search has broad keywords and no domains.
        var plan = plan(DOMAIN_PRIMARY, List.of("Prometheus"), List.of("internet-search"), List.of(), "q");

        assertEquals(List.of("obs-advisor"), plan.circle(), "keywords never widen the match");
    }

    private static final String MIXED_CREW = """
            [{"name":"proxmox-pve","role":"tooler","domains":["proxmox"]},
             {"name":"proxmox-advisor","role":"analyst","domains":["proxmox"]},
             {"name":"k8s-advisor","role":"analyst","domains":["kubernetes"]},
             {"name":"compute","role":"analyst","keywords":["count","sum"]}]
            """;

    @Test
    void mixedCrew_anAnalystWithoutDomainsReviewsEveryDiscussionAsAGeneralist() throws Exception {
        var plan = ReviewConvening.plan(DOMAIN_PRIMARY, MAPPER.readTree(MIXED_CREW), List.of("proxmox"),
                List.of(PROXMOX_PVE), List.of(), "Describe the proxmox nodes.");

        assertEquals(Set.of(PROXMOX_ADVISOR, "compute"), Set.copyOf(plan.circle()));
        assertEquals(ReviewConvening.BASIS_DOMAIN, plan.basis());
    }

    @Test
    void mixedCrew_aQuestionNoDomainTouchesGetsTheBestRankedAnalystAndTheGeneralist() throws Exception {
        var plan = ReviewConvening.plan(DOMAIN_PRIMARY, MAPPER.readTree(MIXED_CREW), List.of(), List.of(),
                List.of(), "Is kubernetes healthy?");

        assertEquals(Set.of(K8S_ADVISOR, "compute"), Set.copyOf(plan.circle()));
        assertEquals(ReviewConvening.BASIS_PRIMARY, plan.basis());
    }

    @Test
    void mixedCrew_withNothingToRankByEveryAnalystReviews() throws Exception {
        var plan = ReviewConvening.plan(DOMAIN_PRIMARY, MAPPER.readTree(MIXED_CREW), List.of(), List.of(),
                List.of(), "Hello");

        assertTrue(plan.circle().isEmpty());
        assertEquals(ReviewConvening.BASIS_FALLBACK_ALL, plan.basis());
    }

    @Test
    void aCrewOfGeneralistsOnlyHasEveryAnalystReview() throws Exception {
        var crew = MAPPER.readTree("[{\"name\":\"a\",\"role\":\"analyst\"},{\"name\":\"b\",\"role\":\"analyst\"}]");

        var plan = ReviewConvening.plan(DOMAIN_PRIMARY, crew, List.of("x"), List.of(), List.of(), "q");

        assertTrue(plan.circle().isEmpty());
    }

    @Test
    void gatherers_areTheAgentsThatAgreedOrObjectedAndTheSelectedToolers() {
        var names = ReviewConvening.gatherers(List.of("a"), List.of("b"), Set.of("c", PROXMOX_ADVISOR),
                Set.of(PROXMOX_ADVISOR));

        assertEquals(Set.of("a", "b", "c"), names);
        assertEquals(Set.of("a"), ReviewConvening.gatherers(List.of("a"), List.of(), null, Set.of()));
    }

    @Test
    void domainMode_theCoordinatorsOwnAnalystSelectionIsKeptAlongsideTheMatches() throws Exception {
        var plan = plan(DOMAIN_PRIMARY, List.of("proxmox"), List.of(PROXMOX_PVE), List.of(GPU_ADVISOR), "q");

        assertEquals(Set.of(GPU_ADVISOR, PROXMOX_ADVISOR), Set.copyOf(plan.circle()));
    }

    @Test
    void noMatch_primaryFallbackConvenesTheBestRankedAnalystFromTheQuestionWords() throws Exception {
        var plan = plan(DOMAIN_PRIMARY, List.of("unknown"), List.of("internet-search"), List.of(),
                "Is CUDA installed on the host?");

        assertEquals(List.of(GPU_ADVISOR), plan.circle());
        assertEquals(ReviewConvening.BASIS_PRIMARY, plan.basis());
    }

    @Test
    void noMatch_primaryFallbackWithNothingToRankByConvenesEveryAnalystNeverNone() throws Exception {
        var plan = plan(DOMAIN_PRIMARY, List.of(), List.of(), List.of(), "Hello there");

        assertTrue(plan.circle().isEmpty(), "an empty circle wakes every analyst");
        assertEquals(ReviewConvening.BASIS_FALLBACK_ALL, plan.basis());
    }

    @Test
    void noMatch_allFallbackConvenesEveryAnalyst() throws Exception {
        var plan = plan(DOMAIN_ALL, List.of("unknown"), List.of(), List.of(), "cuda?");

        assertTrue(plan.circle().isEmpty());
        assertEquals(ReviewConvening.BASIS_FALLBACK_ALL, plan.basis());
    }

    @Test
    void allMode_keepsTodaysBehavior() throws Exception {
        var none = plan(ReviewConvening.Policy.LEGACY, List.of("proxmox"), List.of(PROXMOX_PVE), List.of(), "q");
        assertTrue(none.circle().isEmpty());
        assertEquals(ReviewConvening.BASIS_ALL, none.basis());

        var picked = plan(ReviewConvening.Policy.LEGACY, List.of("proxmox"), List.of(PROXMOX_PVE), List.of(K8S_ADVISOR), "q");
        assertEquals(List.of(K8S_ADVISOR), picked.circle());
        assertEquals(ReviewConvening.BASIS_SELECTED, picked.basis());
    }

    @Test
    void aCrewWithoutAnalystsHasNoCircleInAnyMode() throws Exception {
        var toolersOnly = MAPPER.readTree("[{\"name\":\"k8s-nodes\",\"role\":\"tooler\",\"domains\":[\"kubernetes\"]}]");

        var plan = ReviewConvening.plan(DOMAIN_PRIMARY, toolersOnly, List.of("kubernetes"), List.of(K8S_NODES), List.of(), "q");

        assertTrue(plan.circle().isEmpty());
    }

    @Test
    void missingOrMalformedResumesGiveNoCircle() throws Exception {
        assertTrue(ReviewConvening.plan(DOMAIN_PRIMARY, null, List.of("x"), List.of(), List.of(), "q").circle().isEmpty());
        assertTrue(ReviewConvening.plan(DOMAIN_PRIMARY, MAPPER.readTree("{}"), List.of("x"), List.of(), List.of(), "q")
                .circle().isEmpty());
    }

    @Test
    void policyReadsTheTwoSettingsAndAnythingElseKeepsTodaysBehavior() {
        assertEquals(DOMAIN_ALL, ReviewConvening.policy(Optional.of(" Domain "), Optional.of("ALL")));
        assertEquals(DOMAIN_PRIMARY, ReviewConvening.policy(Optional.of("domain"), Optional.of("primary")));
        assertEquals(ReviewConvening.Policy.LEGACY, ReviewConvening.policy(Optional.empty(), Optional.empty()));
        assertEquals(ReviewConvening.Policy.LEGACY, ReviewConvening.policy(Optional.of("bogus"), Optional.of("bogus")));
    }

    private static final ReviewConvening.Policy DOMAIN_SELECTED = new ReviewConvening.Policy(
            ReviewConvening.Convene.DOMAIN, ReviewConvening.Fallback.PRIMARY, ReviewConvening.Generalists.SELECTED);

    @Test
    void generalistsSelected_aGeneralistTheCoordinatorDidNotSelectDoesNotReview() throws Exception {
        var plan = ReviewConvening.plan(DOMAIN_SELECTED, MAPPER.readTree(MIXED_CREW), List.of("proxmox"),
                List.of(PROXMOX_PVE), List.of(), "Describe the proxmox nodes.");

        assertEquals(List.of(PROXMOX_ADVISOR), plan.circle());
        assertEquals(ReviewConvening.BASIS_DOMAIN, plan.basis());
    }

    @Test
    void generalistsSelected_aGeneralistTheCoordinatorSelectedStillReviews() throws Exception {
        var plan = ReviewConvening.plan(DOMAIN_SELECTED, MAPPER.readTree(MIXED_CREW), List.of("proxmox"),
                List.of(PROXMOX_PVE), List.of("compute"), "Which node has the most memory?");

        assertEquals(Set.of(PROXMOX_ADVISOR, "compute"), Set.copyOf(plan.circle()));
    }

    @Test
    void generalistsSelected_aCrewOfGeneralistsOnlyStillHasEveryAnalystReview() throws Exception {
        var crew = MAPPER.readTree("[{\"name\":\"a\",\"role\":\"analyst\"},{\"name\":\"b\",\"role\":\"analyst\"}]");

        var plan = ReviewConvening.plan(DOMAIN_SELECTED, crew, List.of("x"), List.of(), List.of(), "q");

        assertTrue(plan.circle().isEmpty(), "an empty circle means every analyst reviews");
    }

    @Test
    void generalistsSelected_aQuestionNoDomainTouchesStillGetsTheBestRankedAnalyst() throws Exception {
        var plan = ReviewConvening.plan(DOMAIN_SELECTED, MAPPER.readTree(MIXED_CREW), List.of(), List.of(),
                List.of(), "Is kubernetes healthy?");

        assertEquals(List.of(K8S_ADVISOR), plan.circle());
        assertEquals(ReviewConvening.BASIS_PRIMARY, plan.basis());
    }

    @Test
    void policyReadsTheGeneralistsSettingAndAnythingElseKeepsGeneralistsReviewing() {
        assertEquals(ReviewConvening.Generalists.SELECTED, ReviewConvening.policy(
                Optional.of("domain"), Optional.of("primary"), Optional.of(" Selected ")).generalists());
        assertEquals(ReviewConvening.Generalists.ALWAYS, ReviewConvening.policy(
                Optional.of("domain"), Optional.of("primary"), Optional.of("always")).generalists());
        assertEquals(ReviewConvening.Generalists.ALWAYS, ReviewConvening.policy(
                Optional.of("domain"), Optional.of("primary"), Optional.of("bogus")).generalists());
        assertEquals(ReviewConvening.Generalists.ALWAYS, ReviewConvening.policy(
                Optional.empty(), Optional.empty(), Optional.empty()).generalists());
    }

    @Test
    void tokensAreLowerCaseAlphanumericWordsWithoutOneLetterFragments() {
        assertEquals(Set.of("proxmox", "ve"), ReviewConvening.tokens("Proxmox-VE 8"));
        assertTrue(ReviewConvening.tokens(null).isEmpty());
        assertTrue(ReviewConvening.tokens("").isEmpty());
    }
}
