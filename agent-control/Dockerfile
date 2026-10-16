FROM golang:1.27.2-alpine AS build
ARG VERSION=unreleased
ARG COMMIT_SHA=unknown
WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
COPY agent-protocol/go.mod agent-protocol/go.sum ./agent-protocol/
COPY agent-control/go.mod agent-control/go.sum ./agent-control/
WORKDIR /src/agent-control
RUN go mod download
COPY backend/kitex_gen/ /src/backend/kitex_gen/
COPY agent-protocol/ /src/agent-protocol/
COPY agent-control/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X teamie/agent-control/internal/control.Version=$VERSION -X teamie/agent-control/internal/control.SHA=$COMMIT_SHA" -o /out/service ./cmd
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S teamie && adduser -S -G teamie teamie
WORKDIR /app
COPY --from=build /out/service /app/service
USER teamie
ENTRYPOINT ["/app/service"]
