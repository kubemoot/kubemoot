# Build stage
FROM node:26-alpine@sha256:0b36e8c136b94cd4fcf02188228e76c31ad5872eef3fec8cbd2eee500cfd9e80 AS builder

WORKDIR /app

# Copy package files
COPY package*.json ./

# Install dependencies
RUN npm ci

# Copy source
COPY . .

# Quality gates (svelte-check, ESLint, tests) run inside the build so the image is
# the single source of truth and the CI runner needs no Node toolchain (avoids the
# runner libatomic/Node 26 coupling). A failing check, lint, or test fails the build.
RUN npm run check
RUN npm run lint
RUN npm test

# Build the application
RUN npm run build

# Production stage
FROM node:26-alpine@sha256:0b36e8c136b94cd4fcf02188228e76c31ad5872eef3fec8cbd2eee500cfd9e80

WORKDIR /app

# Create non-root user
RUN addgroup -g 1001 -S nodejs && \
    adduser -S sveltekit -u 1001 -G nodejs

# Copy built application
COPY --from=builder --chown=sveltekit:nodejs /app/build ./build
COPY --from=builder --chown=sveltekit:nodejs /app/package*.json ./

# Install production dependencies only
RUN npm ci --omit=dev && npm cache clean --force

USER sveltekit

# Environment variables
ENV NODE_ENV=production
ENV PORT=3000
ENV HOST=0.0.0.0

EXPOSE 3000

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:3000/dashboard/api/health || exit 1

CMD ["node", "build"]
