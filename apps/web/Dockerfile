FROM node:24-alpine AS build
ARG VERSION=development
ARG COMMIT_SHA=unknown
WORKDIR /workspace/apps/web
COPY apps/web/package*.json ./
RUN npm ci
COPY apps/web/ ./
COPY packages/api-client/ /workspace/packages/api-client/
RUN npm run build
RUN VERSION="$VERSION" COMMIT_SHA="$COMMIT_SHA" node -e 'require("fs").writeFileSync("dist/version.json", JSON.stringify({version:process.env.VERSION,sha:process.env.COMMIT_SHA}))'
FROM nginx:1.28-alpine
ARG VERSION=development
ARG COMMIT_SHA=unknown
LABEL org.opencontainers.image.version=$VERSION org.opencontainers.image.revision=$COMMIT_SHA
COPY apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /workspace/apps/web/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1/healthz || exit 1
