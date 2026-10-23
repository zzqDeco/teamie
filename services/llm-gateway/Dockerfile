FROM golang:1.27.2-alpine AS build
ARG VERSION=unreleased
ARG COMMIT_SHA=unknown
WORKDIR /src
COPY pkg/go.mod pkg/go.sum ./pkg/
COPY contracts/gen/agent/go.mod contracts/gen/agent/go.sum ./contracts/gen/agent/
COPY services/llm-gateway/go.mod services/llm-gateway/go.sum ./services/llm-gateway/
WORKDIR /src/services/llm-gateway
RUN go mod download
COPY pkg/ /src/pkg/
COPY contracts/gen/agent/ /src/contracts/gen/agent/
COPY services/llm-gateway/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X teamie/pkg/httpx.BuildVersion=$VERSION -X teamie/pkg/httpx.BuildSHA=$COMMIT_SHA" -o /service ./cmd

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /service /app/service
ENTRYPOINT ["/app/service"]
