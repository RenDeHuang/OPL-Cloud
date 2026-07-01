FROM node:22-bookworm-slim AS build

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:22-bookworm-slim AS runtime

WORKDIR /app
ENV NODE_ENV=production
ENV PORT=8787

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl \
  && curl -fsSL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/v1.30.8/bin/linux/amd64/kubectl" \
  && chmod +x /usr/local/bin/kubectl \
  && apt-get purge -y --auto-remove curl \
  && rm -rf /var/lib/apt/lists/*

COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY --from=build /app/dist ./dist
COPY services ./services
RUN mkdir -p /app/.runtime && chown -R node:node /app/.runtime

USER node
EXPOSE 8787
CMD ["node", "services/api/server.js"]
