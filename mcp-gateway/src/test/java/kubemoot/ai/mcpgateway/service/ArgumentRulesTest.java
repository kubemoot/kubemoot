package kubemoot.ai.mcpgateway.service;

import kubemoot.ai.mcpgateway.model.ToolOverride;
import kubemoot.ai.mcpgateway.model.ToolOverride.ArgumentRule;
import kubemoot.ai.mcpgateway.model.ToolOverride.ParameterOverride;
import org.junit.jupiter.api.Test;

import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertSame;

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
    void aPatternThatDoesNotCompileIsSkippedAndTheOthersStillApply() {
        ArgumentRules r = rules(new ArgumentRule("(", "broken"), new ArgumentRule("ok", "works"));
        assertEquals(Optional.empty(), check(r, "(("));
        assertEquals(Optional.of("works"), check(r, "ok"));
    }

    @Test
    void incompleteRulesAreSkipped() {
        ArgumentRules r = rules(new ArgumentRule(null, "m"), new ArgumentRule("", "m"),
            new ArgumentRule("x", null), null);
        assertSame(ArgumentRules.none(), r);
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
