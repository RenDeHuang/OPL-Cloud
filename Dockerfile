FROM golang:1.22-bookworm AS provisioner-build

WORKDIR /src/services/fabric
COPY services/internal/postgresmigrate /src/services/internal/postgresmigrate
COPY services/fabric/go.mod services/fabric/go.sum ./
RUN go mod download
COPY services/fabric ./
RUN go build -o /out/opl-tencent-provisioner ./cmd/opl-tencent-provisioner

FROM golang:1.22-bookworm AS control-plane-build

WORKDIR /src/services/control-plane
COPY services/internal/postgresmigrate /src/services/internal/postgresmigrate
COPY services/control-plane/go.mod ./
COPY services/control-plane ./
RUN CGO_ENABLED=0 go build -o /out/opl-control-plane ./cmd/control-plane

FROM golang:1.22-bookworm AS ledger-build

WORKDIR /src/services/ledger
COPY services/internal/postgresmigrate /src/services/internal/postgresmigrate
COPY services/ledger/go.mod services/ledger/go.sum ./
RUN go mod download
COPY services/ledger ./
RUN go build -o /out/opl-ledger ./cmd/ledger

FROM golang:1.22-bookworm AS fabric-build

WORKDIR /src/services/fabric
COPY services/internal/postgresmigrate /src/services/internal/postgresmigrate
COPY services/fabric/go.mod services/fabric/go.sum ./
RUN go mod download
COPY services/fabric ./
RUN go build -o /out/opl-fabric ./cmd/fabric

FROM node:22-bookworm-slim AS build

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-retry-maxtimeout=120000
COPY . .
RUN npm run build

FROM node:22-bookworm-slim AS runtime

WORKDIR /app
ENV NODE_ENV=production
ENV CONTROL_PLANE_ADDR=:8787

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl \
  && curl -fsSL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/v1.30.8/bin/linux/amd64/kubectl" \
  && chmod +x /usr/local/bin/kubectl \
  && apt-get purge -y --auto-remove curl \
  && rm -rf /var/lib/apt/lists/*

COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-retry-maxtimeout=120000
COPY --from=build /app/dist ./dist
COPY packages ./packages
COPY --from=provisioner-build /out/opl-tencent-provisioner /usr/local/bin/opl-tencent-provisioner
COPY --from=control-plane-build /out/opl-control-plane /usr/local/bin/opl-control-plane
COPY --from=ledger-build /out/opl-ledger /usr/local/bin/opl-ledger
COPY --from=fabric-build /out/opl-fabric /usr/local/bin/opl-fabric
RUN mkdir -p /app/.runtime && chown -R node:node /app/.runtime

USER node
EXPOSE 8787
CMD ["/usr/local/bin/opl-control-plane"]
