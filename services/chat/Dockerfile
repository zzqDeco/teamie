FROM golang:1.27.2-alpine AS build
ARG VERSION=unreleased
ARG COMMIT_SHA=unknown
WORKDIR /src
COPY pkg/go.mod pkg/go.sum ./pkg/
COPY contracts/gen/im/go.mod contracts/gen/im/go.sum ./contracts/gen/im/
COPY services/chat/go.mod services/chat/go.sum ./services/chat/
WORKDIR /src/services/chat
RUN go mod download
COPY pkg/ /src/pkg/
COPY contracts/gen/im/ /src/contracts/gen/im/
COPY services/chat/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X teamie/pkg/httpx.BuildVersion=$VERSION -X teamie/pkg/httpx.BuildSHA=$COMMIT_SHA" -o /out/service ./cmd

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S teamie && adduser -S -G teamie teamie
WORKDIR /app
COPY --from=build /out/ /app/
USER teamie
ENTRYPOINT ["/app/service"]
