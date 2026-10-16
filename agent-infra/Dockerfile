FROM golang:1.27.2-alpine AS build
ARG SERVICE
ARG VERSION=unreleased
ARG COMMIT_SHA=unknown
WORKDIR /src
COPY backend/go.mod backend/go.sum backend/
COPY agent-protocol/go.mod agent-protocol/go.sum agent-protocol/
COPY agent-infra/go.mod agent-infra/go.sum agent-infra/
WORKDIR /src/agent-infra
RUN go mod download
COPY backend/ /src/backend/
COPY agent-protocol/ /src/agent-protocol/
COPY agent-infra/ /src/agent-infra/
RUN test "$SERVICE" = llm-gateway -o "$SERVICE" = sandbox && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X teamie/agent-infra/internal/api.BuildVersion=$VERSION -X teamie/agent-infra/internal/api.BuildSHA=$COMMIT_SHA" -o /service ./cmd/$SERVICE
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /service /app/service
ENTRYPOINT ["/app/service"]
