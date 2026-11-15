package kubemoot.ai.mcpgateway.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import io.micrometer.core.instrument.Metrics;
import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import kubemoot.ai.mcpgateway.model.ToolOverride;
import kubemoot.ai.mcpgateway.model.ToolOverride.ArgumentRule;
import kubemoot.ai.mcpgateway.model.ToolOverride.ParameterOverride;
import org.junit.jupiter.api.Test;

import java.time.Duration;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTimeoutPreemptively;

class ArgumentRulesTest {

    private static final String TOOL = "resources_list";
    private static final String PARAM = "fieldSelector";

    private static ArgumentRules rules(ArgumentRule... rules) {
        ToolOverride override = new ToolOverride(TOOL, null,
            Map.of(PARAM, new ParameterOverride("d", Arrays.asList(rules))));
        return ArgumentRules.compile(List.of(override), "k8s");
    }

    private static Optional<String> check(ArgumentRules rules, Object value) {
        Map<String, Object> arguments = new HashMap<>();
        arguments.put(PARAM, value);
        return rules.check(TOOL, arguments);
    }

    @Test
    void aMatchingStringReturnsTheMessage() {
        ArgumentRules r = rules(new ArgumentRule("=~|!~", "no regex"));
        assertEquals(Optional.of("no regex"), check(r, "metadata.namespace!~^ku"));
    }

    @Test
    void theMatchIsASearchNotAFullMatch() {
        ArgumentRules r = rules(new ArgumentRule("[<>]", "no comparison"));
        assertEquals(Optional.of("no comparison"), check(r, "status.capacity.memory>100"));
        assertEquals(Optional.empty(), check(r, "metadata.name=a"));
    }

    @Test
    void anAnchorMakesItAFullMatch() {
        ArgumentRules r = rules(new ArgumentRule("^[^=]*$", "needs ="));
        assertEquals(Optional.of("needs ="), check(r, "spec.nodeName"));
        assertEquals(Optional.empty(), check(r, "spec.nodeName=n1"));
    }

    @Test
    void theFirstMatchingRuleAnswers() {
        ArgumentRules r = rules(new ArgumentRule("zzz", "never"), new ArgumentRule("a", "first"),
            new ArgumentRule("a", "second"));
        assertEquals(Optional.of("first"), check(r, "a"));
    }

    @Test
    void anAbsentNullEmptyOrNonStringArgumentNeverMatches() {
        ArgumentRules r = rules(new ArgumentRule(".*", "everything"));
        assertEquals(Optional.empty(), r.check(TOOL, Map.of()));
        assertEquals(Optional.empty(), r.check(TOOL, null));
        assertEquals(Optional.empty(), check(r, null));
        assertEquals(Optional.empty(), check(r, ""));
        assertEquals(Optional.empty(), check(r, 12));
        assertEquals(Optional.empty(), check(r, List.of("a=b")));
        assertEquals(Optional.empty(), check(r, Map.of("a", "b")));
    }

    @Test
    void aRuleBindsToItsToolAndParameterOnly() {
        ArgumentRules r = rules(new ArgumentRule("x", "no"));
        assertEquals(Optional.empty(), r.check("pods_list", Map.of(PARAM, "x")));
        assertEquals(Optional.empty(), r.check(TOOL, Map.of("labelSelector", "x")));
    }

    @Test
    void aPatternThatDoesNotCompileFailsClosedWithItsOwnMessage() {
        ArgumentRules r = rules(new ArgumentRule("(", "broken rule"), new ArgumentRule("ok", "works"));
        assertEquals(1, r.invalidRules());
        assertEquals(Optional.of("broken rule"), check(r, "anything at all"));
        assertEquals(Optional.empty(), check(r, ""));
        assertEquals(Optional.empty(), check(r, 7));
    }

    @Test
    void incompleteRulesFailClosedToo() {
        for (ArgumentRule rule : new ArgumentRule[] {new ArgumentRule(null, "m"), new ArgumentRule("", "m"),
            new ArgumentRule("x", null)}) {
            ArgumentRules r = rules(rule);
            assertEquals(1, r.invalidRules(), String.valueOf(rule));
            assertEquals(true, check(r, "y").isPresent(), String.valueOf(rule));
        }
    }

    @Test
    void anInvalidRuleIsCountedInTheMetric() {
        SimpleMeterRegistry registry = new SimpleMeterRegistry();
        Metrics.addRegistry(registry);
        try {
            rules(new ArgumentRule("(", "m"), new ArgumentRule("[", "m"));
            assertEquals(2.0, registry.get("mcpgateway.argument_rules.invalid").tag("server", "k8s")
                .counter().count());
        } finally {
            Metrics.removeRegistry(registry);
        }
    }

    @Test
    void parametersAreCheckedInTheOrderTheOverrideListsThem() {
        Map<String, ParameterOverride> parameters = new LinkedHashMap<>();
        parameters.put("zeta", new ParameterOverride("d", List.of(new ArgumentRule("x", "zeta first"))));
        parameters.put("alpha", new ParameterOverride("d", List.of(new ArgumentRule("x", "alpha second"))));
        ArgumentRules r = ArgumentRules.compile(List.of(new ToolOverride(TOOL, null, parameters)), "k8s");
        assertEquals(Optional.of("zeta first"), r.check(TOOL, Map.of("alpha", "x", "zeta", "x")));
    }

    @Test
    void aNestedStringIsNotScanned() {
        ArgumentRules r = rules(new ArgumentRule(".*", "everything"));
        assertEquals(Optional.empty(), r.check(TOOL, Map.of("outer", Map.of(PARAM, "x"))));
    }

    @Test
    void matchingTimeIsLinearSoABacktrackingPatternFinishesAtOnce() {
        ArgumentRules r = rules(new ArgumentRule("(.*a){12}$", "slow"));
        String input = "a".repeat(29) + "!";
        assertTimeoutPreemptively(Duration.ofSeconds(2), () -> assertEquals(Optional.empty(), check(r, input)));
        String longer = "a".repeat(30_000) + "!";
        assertTimeoutPreemptively(Duration.ofSeconds(5), () -> check(r, longer));
    }

    // The RE2 dialect, where java.util.regex differs. Each case pins the RE2 answer.

    @Test
    void posixClassesInsideBracketsAreRe2() {
        ArgumentRules r = rules(new ArgumentRule("^[[:alpha:]]$", "m"));
        assertEquals(true, check(r, "a").isPresent());
        assertEquals(Optional.empty(), check(r, ":"));
        assertEquals(Optional.empty(), check(r, "1"));
    }

    @Test
    void theUFlagMeansUngreedyNotUnicodeClasses() {
        ArgumentRules r = rules(new ArgumentRule("(?U)^\\w$", "m"));
        assertEquals(Optional.empty(), check(r, "\u00e9"), "\\w stays ASCII");
        assertEquals(true, check(r, "e").isPresent());
    }

    @Test
    void dollarMatchesOnlyAtTheEndOfTheText() {
        ArgumentRules r = rules(new ArgumentRule("a$", "m"));
        assertEquals(true, check(r, "xa").isPresent());
        assertEquals(Optional.empty(), check(r, "a\n"), "no match before a final newline");
    }

    @Test
    void dotMatchesACarriageReturnButNotANewline() {
        ArgumentRules r = rules(new ArgumentRule("^a.b$", "m"));
        assertEquals(true, check(r, "a\rb").isPresent());
        assertEquals(Optional.empty(), check(r, "a\nb"));
    }

    @Test
    void whitespaceClassExcludesTheVerticalTab() {
        ArgumentRules r = rules(new ArgumentRule("^\\s$", "m"));
        assertEquals(true, check(r, " ").isPresent());
        assertEquals(true, check(r, "\f").isPresent());
        assertEquals(Optional.empty(), check(r, "\u000B"));
    }

    @Test
    void caseInsensitiveFlagFoldsUnicodeSimply() {
        ArgumentRules r = rules(new ArgumentRule("(?i)memory", "m"));
        assertEquals(true, check(r, "MEMORY").isPresent());
        assertEquals(true, check(r, "MeMoRy").isPresent());
        ArgumentRules kelvin = rules(new ArgumentRule("(?i)^k$", "m"));
        assertEquals(true, check(kelvin, "\u212A").isPresent(), "the Kelvin sign folds to k");
    }

    @Test
    void noOverridesOrNoRulesGivesNone() {
        assertSame(ArgumentRules.none(), ArgumentRules.compile(null, "k8s"));
        assertSame(ArgumentRules.none(), ArgumentRules.compile(List.of(), "k8s"));
        assertSame(ArgumentRules.none(), ArgumentRules.compile(
            List.of(new ToolOverride(TOOL, "text", Map.of(PARAM, new ParameterOverride("d")))), "k8s"));
        assertEquals(Optional.empty(), ArgumentRules.none().check(TOOL, Map.of(PARAM, "x")));
    }

    @Test
    void theHomelabPilotSelectorRulesBehaveAsTheSharedFixtureSays() throws Exception {
        JsonNode fixture = new ObjectMapper().readTree(
            getClass().getResourceAsStream("/crew-rules/homelab-pilot-resources-list-field-selector.json"));
        List<ArgumentRule> rules = new ArrayList<>();
        int index = 0;
        for (JsonNode rule : fixture.get("rules")) {
            rules.add(new ArgumentRule(rule.get("reject").asText(), "rule-" + index++));
        }
        ToolOverride override = new ToolOverride(fixture.get("tool").asText(), null,
            Map.of(fixture.get("parameter").asText(), new ParameterOverride("d", rules)));
        ArgumentRules compiled = ArgumentRules.compile(List.of(override), "k8s");
        assertEquals(0, compiled.invalidRules());
        String param = fixture.get("parameter").asText();
        for (JsonNode c : fixture.get("reject")) {
            assertEquals(Optional.of("rule-" + c.get("rule").asInt()),
                compiled.check(override.name(), Map.of(param, c.get("input").asText())), c.get("input").asText());
        }
        for (JsonNode input : fixture.get("pass")) {
            assertEquals(Optional.empty(), compiled.check(override.name(), Map.of(param, input.asText())),
                input.asText());
        }
        assertEquals(4, rules.size());
    }

    @Test
    void theKubernetesSelectorRulesCatchTheObservedMistakes() {
        ArgumentRules r = rules(
            new ArgumentRule("=~|!~", "regex"),
            new ArgumentRule("[<>]", "comparison"),
            new ArgumentRule("(^|,)[^=,]+(,|$)", "bare"),
            new ArgumentRule("(memory|cpu|capacity|allocatable)", "metrics"));
        assertEquals(Optional.of("regex"), check(r, "metadata.namespace!~^ku"));
        assertEquals(Optional.of("comparison"), check(r, "status.capacity.memory>4"));
        assertEquals(Optional.of("bare"), check(r, "status.allocatable"));
        assertEquals(Optional.of("bare"), check(r, "metadata.name=a,spec.nodeName"));
        assertEquals(Optional.of("metrics"), check(r, "status.allocatable.memory=4"));
        assertEquals(Optional.empty(), check(r, "metadata.namespace!=kube-system,status.phase=Running"));
    }
}
