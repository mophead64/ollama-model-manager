FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X github.com/mophead64/ollama-model-manager/internal/version.Version=${VERSION}" -o /out/ollama-model-manager ./cmd/ollama-model-manager

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ollama-model-manager /ollama-model-manager
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/ollama-model-manager"]
