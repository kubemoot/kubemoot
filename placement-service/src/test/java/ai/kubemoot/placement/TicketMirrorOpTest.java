package ai.kubemoot.placement;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import org.junit.jupiter.api.Test;

class TicketMirrorOpTest {

    @Test
    void aQueuedWriteIsEqualByItsBytesAndAllowsARemovalWithNoValue() throws Exception {
        var type = Class.forName("ai.kubemoot.placement.TicketMirror$Op");
        var ctor = type.getDeclaredConstructor(String.class, byte[].class);
        ctor.setAccessible(true);
        Object a = ctor.newInstance("k", new byte[] {1, 2});
        assertEquals(a, ctor.newInstance("k", new byte[] {1, 2}));
        assertEquals(a.hashCode(), ctor.newInstance("k", new byte[] {1, 2}).hashCode());
        assertNotEquals(a, ctor.newInstance("k", new byte[] {9}));
        assertNotEquals(a, ctor.newInstance("x", new byte[] {1, 2}));
        assertEquals(ctor.newInstance("k", null), ctor.newInstance("k", null));
        assertTrue(a.toString().contains("[1, 2]"));
    }
}
