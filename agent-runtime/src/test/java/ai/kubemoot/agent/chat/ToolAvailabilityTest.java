package ai.kubemoot.agent.chat;

import org.junit.jupiter.api.Test;

import java.util.List;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

class ToolAvailabilityTest {

    @Test
    void noDeclaredTools_isNeverUnmet() {
        var availability = ToolAvailability.of(List.of(), List.of(), List.of());
        assertFalse(availability.unmet(true));
        assertFalse(availability.unmet(false));
    }

    @Test
    void allPresent_isMet() {
        var availability = ToolAvailability.of(List.of("a", "b"), List.of(), List.of("a", "b", "c"));
        assertFalse(availability.anyMissing());
        assertFalse(availability.unmet(true));
    }

    @Test
    void someMissing_failsAToolerButNotAnotherRole() {
        var availability = ToolAvailability.of(List.of("a", "b"), List.of(), List.of("a"));
        assertEquals(List.of("b"), availability.missing());
        assertTrue(availability.unmet(true));
        assertFalse(availability.unmet(false));
    }

    @Test
    void allMissing_failsEveryRole() {
        var availability = ToolAvailability.of(List.of("a", "b"), List.of(), List.of());
        assertTrue(availability.unmet(true));
        assertTrue(availability.unmet(false));
    }

    @Test
    void declaredToolsFollowTheSameRuleAsTheToolFilter() {
        var enabled = List.of("a", "b", "c");
        var disabled = List.of("b");
        var expected = enabled.stream()
                .filter(name -> ai.kubemoot.agent.mcp.McpClientService.isServerToolAllowed(name, enabled, disabled))
                .toList();
        assertEquals(expected, ToolAvailability.of(enabled, disabled, List.of()).declared());
    }

    @Test
    void aServerQualifiedNameMatchesAsIs() {
        var availability = ToolAvailability.of(List.of("srv.search"), List.of(), List.of("srv.search"));
        assertFalse(availability.anyMissing());
    }

    @Test
    void aDisabledToolIsNotDeclared_andADuplicateCountsOnce() {
        var availability = ToolAvailability.of(List.of("a", "a", "b"), List.of("b"), List.of());
        assertEquals(List.of("a"), availability.declared());
        assertEquals(List.of("a"), availability.missing());
    }
}
