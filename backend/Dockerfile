FROM golang:1.27.2-alpine AS build
ARG SERVICE
ARG VERSION=unreleased
ARG COMMIT_SHA=unknown
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN test "$SERVICE" = identity -o "$SERVICE" = chat -o "$SERVICE" = realtime \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X teamie/backend/internal/api.BuildVersion=$VERSION -X teamie/backend/internal/api.BuildSHA=$COMMIT_SHA" -o /out/service ./services/$SERVICE/cmd

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -S teamie && adduser -S -G teamie teamie
WORKDIR /app
COPY --from=build /out/service /app/service
USER teamie
ENTRYPOINT ["/app/service"]
