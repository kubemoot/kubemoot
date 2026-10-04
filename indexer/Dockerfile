FROM eclipse-temurin:25-jdk-alpine@sha256:3fd2d245c4e0eba615fe366a71b8bd25f5db7104f53e4026b24bf508b880bd2a AS builder

WORKDIR /build
COPY gradle gradle
COPY gradlew gradlew
COPY build.gradle.kts settings.gradle.kts ./
RUN chmod +x gradlew && ./gradlew dependencies --no-daemon || true
COPY src src
RUN ./gradlew bootJar --no-daemon

FROM eclipse-temurin:25-jre-alpine@sha256:3c0a9084927a221ccd1d007fcaf614465672c0af37aaa834c5184483afe56d61
WORKDIR /app
COPY --from=builder /build/build/libs/*.jar app.jar
EXPOSE 8080
# Run as the same non-root uid:gid the operator sets in the container
# securityContext. The indexer clones into /data (a volume in the cluster),
# so the image directory is owned by that uid for runs without one.
RUN mkdir -p /data && chown 1000:1000 /data
ENV HOME=/data
USER 1000:1000
ENTRYPOINT ["java", "-jar", "app.jar"]
