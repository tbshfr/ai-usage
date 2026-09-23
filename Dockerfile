FROM --platform=$BUILDPLATFORM golang:1.26.7-trixie AS builder
WORKDIR /app

ARG VERSION="dev"
ARG TARGETOS
ARG TARGETARCH

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o ai-usage ./cmd/ai-usage/

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /app/ai-usage /ai-usage

ENV AI_USAGE_DATA_DIR=/data \
    AI_USAGE_HTTP_ADDR=":8080"

EXPOSE 8080 4318 4317

ENTRYPOINT ["/ai-usage"]
