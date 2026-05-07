# Benchmark Dockerfile: Full-stack Ruby + Node app
# Simulates a Rails backend that serves compiled frontend assets.
#
# Usage:
#   Cold build (no proxy):
#     docker build --no-cache -t bench .
#
#   With sparkwing-proxy:
#     docker build --no-cache --build-arg PROXY_URL=http://host.docker.internal:9777 -t bench .
#
#   Change base image (bust all layers, proxy still helps):
#     docker build --no-cache --build-arg NODE_IMAGE=node:20-alpine --build-arg PROXY_URL=http://host.docker.internal:9777 -t bench .

# ---------- args ----------
ARG NODE_IMAGE=node:22-alpine
ARG RUBY_IMAGE=ruby:3.3-alpine

# ========== Stage 1: Build frontend assets ==========
FROM ${NODE_IMAGE} AS frontend
ARG PROXY_URL=""
WORKDIR /app

COPY package.json ./

# Cache mount at /root/.npm = npm download cache (not install dir).
# npm installs into ./node_modules (in the layer). Cache mount just
# avoids re-downloading tarballs on subsequent builds.
RUN --mount=type=cache,target=/root/.npm \
    set -e; \
    if [ -n "$PROXY_URL" ]; then \
      npm config set registry "${PROXY_URL}/proxy/npm/"; \
      echo "==> npm registry: $(npm config get registry)"; \
    fi; \
    npm install; \
    echo "==> node_modules: $(ls node_modules | wc -l) packages"

# ========== Stage 2: Install Ruby gems ==========
FROM ${RUBY_IMAGE} AS backend
ARG PROXY_URL=""
WORKDIR /app

RUN apk add --no-cache build-base postgresql-dev linux-headers gcompat

COPY Gemfile Gemfile.lock ./

# Cache mount strategy for bundler:
#   - Mount cache at /bundle-cache (separate from install dir)
#   - Before install: restore any previously compiled gems from cache
#   - After install: save compiled gems back to cache
#   - /usr/local/bundle stays in the layer so COPY works in stage 3
RUN --mount=type=cache,target=/bundle-cache \
    if [ -n "$PROXY_URL" ]; then \
      bundle config mirror.https://rubygems.org "${PROXY_URL}/proxy/rubygems/"; \
      echo "==> rubygems mirror: ${PROXY_URL}/proxy/rubygems/"; \
    fi; \
    if [ -d /bundle-cache/ruby ]; then \
      echo "==> restoring gems from cache"; \
      cp -a /bundle-cache/* /usr/local/bundle/ 2>/dev/null || true; \
    fi; \
    bundle install --jobs=4 || echo "==> bundle install exited $? (some native exts may fail on this platform)"; \
    cp -a /usr/local/bundle/* /bundle-cache/ 2>/dev/null || true; \
    echo "==> gems in bundle dir: $(ls /usr/local/bundle/gems/ 2>/dev/null | wc -l)"

# ========== Stage 3: Final image ==========
FROM ${RUBY_IMAGE}
WORKDIR /app

RUN apk add --no-cache postgresql-libs gcompat

# Copy gems from backend stage (now in the layer, not hidden in a cache mount)
COPY --from=backend /usr/local/bundle /usr/local/bundle

# Copy compiled frontend assets from frontend stage
COPY --from=frontend /app/node_modules /app/public/assets

# Placeholder entrypoint
CMD ["echo", "bench build complete"]
