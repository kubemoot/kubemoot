package ai.kubemoot.agent.chat;

import static org.junit.jupiter.api.Assertions.assertEquals;

import org.junit.jupiter.api.Test;

class CallManifestEdgeSpaceTest {

    @Test
    void dropsOneLeadingAndOneTrailingSpace() {
        assertEquals("a b", CallManifest.withoutEdgeSpace(" a b "));
        assertEquals("a b", CallManifest.withoutEdgeSpace(" a b"));
        assertEquals("a b", CallManifest.withoutEdgeSpace("a b "));
    }

    @Test
    void keepsInnerSpaceAndTextWithoutEdges() {
        assertEquals("a b", CallManifest.withoutEdgeSpace("a b"));
        assertEquals("ab", CallManifest.withoutEdgeSpace("ab"));
    }

    @Test
    void emptyAndSingleSpaceBecomeEmpty() {
        assertEquals("", CallManifest.withoutEdgeSpace(""));
        assertEquals("", CallManifest.withoutEdgeSpace(" "));
    }

    @Test
    void removesOnlyOneSpaceAtEachEdge() {
        assertEquals(" a ", CallManifest.withoutEdgeSpace("  a  "));
    }
}
