FROM golang:1.27.2-alpine AS build
ARG VERSION=unreleased
ARG COMMIT_SHA=unknown
WORKDIR /src
COPY backend/go.mod backend/go.sum backend/
COPY agent-protocol/go.mod agent-protocol/go.sum agent-protocol/
COPY agent-runtime/go.mod agent-runtime/go.sum agent-runtime/
WORKDIR /src/agent-runtime
RUN go mod download
COPY backend/ /src/backend/
COPY agent-protocol/ /src/agent-protocol/
COPY agent-runtime/ /src/agent-runtime/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.BuildVersion=$VERSION -X main.BuildSHA=$COMMIT_SHA" -o /service ./cmd
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S teamie && adduser -S -G teamie teamie
COPY --from=build /service /app/service
USER teamie
ENTRYPOINT ["/app/service"]
