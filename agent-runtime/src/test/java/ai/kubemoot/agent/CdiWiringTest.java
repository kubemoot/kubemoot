package ai.kubemoot.agent;

import io.quarkus.arc.Arc;
import io.quarkus.runtime.StartupEvent;
import io.quarkus.test.junit.QuarkusTest;
import jakarta.enterprise.inject.spi.ObserverMethod;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;

import java.util.Set;
import java.util.stream.Collectors;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * Boots the application through Quarkus ArC and asserts that every bean that
 * starts an agent role resolves and keeps its {@code @Observes StartupEvent}
 * observer. The test profile configures no channels or NATS URL, so the
 * observers return without contacting a model or NATS.
 */
@QuarkusTest
class CdiWiringTest {

    static Stream<Class<?>> startupBeans() {
        return StartupBeans.all();
    }

    private static Set<Class<?>> startupObserverOwners() {
        return Arc.container().beanManager()
                .resolveObserverMethods(new StartupEvent())
                .stream()
                .map(ObserverMethod::getBeanClass)
                .collect(Collectors.toSet());
    }

    @ParameterizedTest
    @MethodSource("startupBeans")
    void startupBeanResolves(Class<?> beanClass) {
        assertTrue(Arc.container().instance(beanClass).isAvailable(),
                beanClass.getSimpleName() + " is not a resolvable CDI bean");
    }

    @ParameterizedTest
    @MethodSource("startupBeans")
    void startupObserverIsRegistered(Class<?> beanClass) {
        assertTrue(startupObserverOwners().contains(beanClass),
                beanClass.getSimpleName() + " lost its @Observes StartupEvent observer");
    }

    @Test
    void classOutsideTheBeanGraphDoesNotResolve() {
        assertFalse(Arc.container().instance(NotABean.class).isAvailable());
        assertFalse(startupObserverOwners().contains(NotABean.class));
    }

    static final class NotABean {
    }
}
