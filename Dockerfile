FROM golang:1.25-bookworm@sha256:6359592445455f2dbe2412bed411336035bc019a50017720d77454ffdd6d0f82 AS provisioner-build

WORKDIR /src/services/fabric
COPY services/internal/postgresmigrate /src/services/internal/postgresmigrate
COPY services/fabric/go.mod services/fabric/go.sum ./
RUN go mod download
COPY services/fabric ./
RUN go build -o /out/opl-tencent-provisioner ./cmd/opl-tencent-provisioner

FROM golang:1.22-bookworm@sha256:3d699e4d15d0f8f13c9195c0632a16702b8cbdece2955af1c23b37ae5d55a253 AS control-plane-build

WORKDIR /src/services/control-plane
COPY services/internal/postgresmigrate /src/services/internal/postgresmigrate
COPY services/control-plane/go.mod ./
COPY services/control-plane ./
RUN CGO_ENABLED=0 go build -o /out/opl-control-plane ./cmd/control-plane

FROM golang:1.22-bookworm@sha256:3d699e4d15d0f8f13c9195c0632a16702b8cbdece2955af1c23b37ae5d55a253 AS ledger-build

WORKDIR /src/services/ledger
COPY services/internal/postgresmigrate /src/services/internal/postgresmigrate
COPY services/ledger/go.mod services/ledger/go.sum ./
RUN go mod download
COPY services/ledger ./
RUN go build -o /out/opl-ledger ./cmd/ledger

FROM golang:1.25-bookworm@sha256:6359592445455f2dbe2412bed411336035bc019a50017720d77454ffdd6d0f82 AS fabric-build

WORKDIR /src/services/fabric
COPY services/internal/postgresmigrate /src/services/internal/postgresmigrate
COPY services/fabric/go.mod services/fabric/go.sum ./
RUN go mod download
COPY services/fabric ./
RUN go build -o /out/opl-fabric ./cmd/fabric

FROM docker:27.5.1-cli@sha256:851f91d241214e7c6db86513b270d58776379aacc5eb9c4a87e5b47115e3065c AS docker-cli

FROM node:22-bookworm-slim@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436 AS build

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-retry-maxtimeout=120000
COPY . .
RUN npm run build

FROM node:22-bookworm-slim@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436 AS runtime

WORKDIR /app
ARG TARGETARCH
ENV NODE_ENV=production
ENV CONTROL_PLANE_ADDR=:8787

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl \
  && curl -fsSL -o /usr/local/bin/kubectl "https://dl.k8s.io/release/v1.30.8/bin/linux/${TARGETARCH}/kubectl" \
  && case "${TARGETARCH}" in \
       amd64) KUBECTL_SHA256="7f39bdcf768ce4b8c1428894c70c49c8b4d2eee52f3606eb02f5f7d10f66d692" ;; \
       arm64) KUBECTL_SHA256="e51d6a76fade0871a9143b64dc62a5ff44f369aa6cb4b04967d93798bf39d15b" ;; \
       *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
     esac \
  && echo "${KUBECTL_SHA256}  /usr/local/bin/kubectl" | sha256sum -c \
  && chmod +x /usr/local/bin/kubectl \
  && rm -rf /var/lib/apt/lists/*

COPY package.json package-lock.json ./
RUN npm ci --omit=dev --no-audit --no-fund --fetch-retries=5 --fetch-retry-mintimeout=20000 --fetch-retry-maxtimeout=120000
COPY --from=build /app/dist ./dist
COPY packages ./packages
COPY --from=provisioner-build /out/opl-tencent-provisioner /usr/local/bin/opl-tencent-provisioner
COPY --from=control-plane-build /out/opl-control-plane /usr/local/bin/opl-control-plane
COPY --from=ledger-build /out/opl-ledger /usr/local/bin/opl-ledger
COPY --from=fabric-build /out/opl-fabric /usr/local/bin/opl-fabric
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
RUN mkdir -p /app/.runtime && chown -R node:node /app/.runtime

USER node
EXPOSE 8787
CMD ["/usr/local/bin/opl-control-plane"]
