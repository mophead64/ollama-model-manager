# The build stage runs natively and cross-compiles, so building for another
# architecture (e.g. linux/amd64 on an Apple silicon Mac) needs no emulation.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS=linux TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/mophead64/ollama-model-manager/internal/version.Version=${VERSION}" -o /out/ollama-model-manager ./cmd/ollama-model-manager
RUN mkdir -p /out/data

# base rather than static: it has glibc, which nvidia-smi needs when the NVIDIA
# Container Toolkit mounts it in (docker run --gpus all) for GPU stats.
FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/ollama-model-manager /ollama-model-manager
# A named volume is seeded from the image's directory (content *and*
# ownership) the first time it's mounted. distroless has no shell to chown
# at runtime, so /data has to already be owned by nonroot here, otherwise
# Docker creates the volume root-owned and the app can't open its db file.
COPY --from=build --chown=nonroot:nonroot /out/data /data
VOLUME ["/data"]
# Outside a container the database defaults to sitting beside the binary;
# here it belongs in the volume.
ENV DB_PATH=/data/omm.db
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/ollama-model-manager"]
