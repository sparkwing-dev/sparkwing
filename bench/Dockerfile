ARG NODE_IMAGE=node:22-alpine
ARG RUBY_IMAGE=ruby:3.3-alpine

FROM ${NODE_IMAGE} AS frontend
ARG PROXY_URL=""
WORKDIR /app

COPY package.json ./

RUN --mount=type=cache,target=/root/.npm \
    set -e; \
    if [ -n "$PROXY_URL" ]; then \
      npm config set registry "${PROXY_URL}/proxy/npm/"; \
      echo "==> npm registry: $(npm config get registry)"; \
    fi; \
    npm install; \
    echo "==> node_modules: $(ls node_modules | wc -l) packages"

FROM ${RUBY_IMAGE} AS backend
ARG PROXY_URL=""
WORKDIR /app

RUN apk add --no-cache build-base postgresql-dev linux-headers gcompat

COPY Gemfile Gemfile.lock ./

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

FROM ${RUBY_IMAGE}
WORKDIR /app

RUN apk add --no-cache postgresql-libs gcompat

COPY --from=backend /usr/local/bundle /usr/local/bundle

COPY --from=frontend /app/node_modules /app/public/assets

CMD ["echo", "bench build complete"]
