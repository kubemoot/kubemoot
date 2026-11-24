package ai.kubemoot.placement;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import ai.kubemoot.placement.MirrorBucket.Item;
import org.junit.jupiter.api.Test;

class ByteRecordsTest {

    @Test
    void anItemIsEqualByItsBytesAndAllowsARemovalWithNoValue() {
        var a = new Item("k", new byte[] {1, 2}, 5);
        assertEquals(a, new Item("k", new byte[] {1, 2}, 5));
        assertEquals(a.hashCode(), new Item("k", new byte[] {1, 2}, 5).hashCode());
        assertNotEquals(a, new Item("k", new byte[] {1, 3}, 5));
        assertNotEquals(a, new Item("j", new byte[] {1, 2}, 5));
        assertNotEquals(a, new Item("k", new byte[] {1, 2}, 6));
        assertEquals(new Item("k", null, 5), new Item("k", null, 5));
        assertTrue(a.toString().contains("[1, 2]"));
    }
}
