package ai.kubemoot.agent.util;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertEquals;

class ThinkBlocksTest {

    @Test
    void removesEveryBlock_caseInsensitive_acrossLines() {
        assertEquals("  answer   more", ThinkBlocks.remove("<think>a\nb</think> answer <THINK>c</THINK> more"));
    }

    @Test
    void textWithoutABlock_isUnchanged_andNullIsEmpty() {
        assertEquals("plain", ThinkBlocks.remove("plain"));
        assertEquals("", ThinkBlocks.remove(null));
    }

    @Test
    void anUnclosedBlock_isLeftAsIs() {
        assertEquals("<think>still thinking", ThinkBlocks.remove("<think>still thinking"));
    }
}
