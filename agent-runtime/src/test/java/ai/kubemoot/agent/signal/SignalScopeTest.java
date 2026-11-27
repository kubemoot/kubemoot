package ai.kubemoot.agent.signal;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertSame;
import static org.junit.jupiter.api.Assertions.assertTrue;

class SignalScopeTest {

    @Test
    void noDeclaredKindsOffersNoTool() {
        try (var scope = SignalScope.bind(DeclaredSignals.NONE)) {
            assertNull(SignalScope.current(), "nothing is bound");
            assertFalse(scope.handles("signal"));
            assertTrue(scope.call().isEmpty());
            assertFalse(scope.declaresNoFinding());
        }
        try (var scope = SignalScope.bind(null)) {
            assertNull(SignalScope.current());
            assertFalse(scope.handles("signal"));
        }
    }

    @Test
    void theToolIsOfferedOnlyWhileBound() {
        try (var scope = SignalScope.bind(SignalTestKinds.consent3())) {
            assertSame(scope, SignalScope.current());
            assertTrue(scope.handles("signal"));
            assertFalse(scope.handles("resources_list"));
            assertEquals("signal", scope.spec().name());
        }
        assertNull(SignalScope.current(), "closing the scope unbinds the tool");
    }

    @Test
    void aNestedScopeRestoresTheOuterOne() {
        try (var outer = SignalScope.bind(SignalTestKinds.consent3())) {
            try (var inner = SignalScope.bind(SignalTestKinds.consent3())) {
                assertSame(inner, SignalScope.current());
            }
            assertSame(outer, SignalScope.current());
        }
        assertNull(SignalScope.current());
    }

    @Test
    void theLastValidCallIsTheVerdictAndARefusedCallKeepsIt() {
        try (var scope = SignalScope.bind(SignalTestKinds.consent3())) {
            scope.handle("{\"kind\":\"agree\",\"statement\":\"7 nodes\"}");
            scope.handle("{\"kind\":\"concern\",\"statement\":\"one is NotReady\"}");
            String refused = scope.handle("{\"kind\":\"vote\",\"statement\":\"x\"}");
            assertTrue(refused.contains("error"));
            assertEquals("concern", scope.call().orElseThrow().kind());
        }
    }

    @Test
    void noDataAToolGapAndAStandAsideDeclareNoFinding() {
        for (String kind : new String[]{"no_data", "tool_gap", "stand_aside"}) {
            try (var scope = SignalScope.bind(SignalTestKinds.consent3())) {
                scope.handle("{\"kind\":\"" + kind + "\",\"statement\":\"x\"}");
                assertTrue(scope.declaresNoFinding(), kind);
            }
        }
        try (var scope = SignalScope.bind(SignalTestKinds.consent3())) {
            scope.handle("{\"kind\":\"agree\",\"statement\":\"x\"}");
            assertFalse(scope.declaresNoFinding());
        }
    }

    @Test
    void theEffectTableMapsTheRuntimesNamesAndNothingElse() {
        assertEquals(SignalEffect.AGREE, SignalEffect.of("agree"));
        assertEquals(SignalEffect.TOOL_GAP, SignalEffect.of("tool_gap"));
        assertEquals(SignalEffect.NO_DATA, SignalEffect.of("no_data"));
        assertEquals(SignalEffect.DECLARED, SignalEffect.of("vote"));
        assertEquals(SignalEffect.DECLARED, SignalEffect.of("AGREE"), "names are exact");
        assertEquals(SignalEffect.DECLARED, SignalEffect.of(null));
    }
}
