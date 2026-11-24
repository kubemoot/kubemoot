package ai.kubemoot.agent;

import jakarta.inject.Inject;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;

import java.lang.reflect.Constructor;
import java.util.Arrays;
import java.util.Optional;
import java.util.stream.Stream;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

/**
 * ArC picks the {@code @Inject} constructor, else the only constructor, so a
 * startup bean needs exactly one unambiguous constructor. Checked on the source
 * classes, before ArC adds its own no-arg constructor for client proxies.
 */
class StartupBeanConstructorTest {

    static Stream<Class<?>> startupBeans() {
        return StartupBeans.all();
    }

    /** Returns why ArC cannot pick a constructor, or empty when it can. */
    static Optional<String> constructorProblem(Class<?> beanClass) {
        Constructor<?>[] ctors = beanClass.getDeclaredConstructors();
        long injectAnnotated = Arrays.stream(ctors)
                .filter(c -> c.isAnnotationPresent(Inject.class))
                .count();
        if (injectAnnotated > 1) {
            return Optional.of(beanClass.getSimpleName() + " has " + injectAnnotated + " @Inject constructors");
        }
        if (injectAnnotated == 0 && ctors.length > 1) {
            return Optional.of(beanClass.getSimpleName() + " has " + ctors.length
                    + " constructors and none is @Inject");
        }
        return Optional.empty();
    }

    @ParameterizedTest
    @MethodSource("startupBeans")
    void startupBeanHasOneUnambiguousConstructor(Class<?> beanClass) {
        assertEquals(Optional.empty(), constructorProblem(beanClass));
    }

    @Test
    void twoUnannotatedConstructorsAreRejected() {
        assertTrue(constructorProblem(TwoPlain.class).isPresent());
    }

    @Test
    void twoInjectConstructorsAreRejected() {
        assertTrue(constructorProblem(TwoInject.class).isPresent());
    }

    @Test
    void oneInjectAmongSeveralIsAccepted() {
        assertEquals(Optional.empty(), constructorProblem(OneInject.class));
    }

    @Test
    void singleConstructorIsAccepted() {
        assertEquals(Optional.empty(), constructorProblem(Single.class));
    }

    static final class TwoPlain {
        final String arg;

        TwoPlain() {
            this(null);
        }

        TwoPlain(String arg) {
            this.arg = arg;
        }
    }

    static final class TwoInject {
        final String arg;

        @Inject
        TwoInject() {
            this(null);
        }

        @Inject
        TwoInject(String arg) {
            this.arg = arg;
        }
    }

    static final class OneInject {
        final String arg;

        OneInject() {
            this(null);
        }

        @Inject
        OneInject(String arg) {
            this.arg = arg;
        }
    }

    static final class Single {
        final String arg;

        Single(String arg) {
            this.arg = arg;
        }
    }
}
