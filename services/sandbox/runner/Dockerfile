# Platform-only execution image. It contains no application configuration or keys.
FROM python:3.13-alpine
RUN addgroup -g 10001 runner && adduser -D -u 10001 -G runner runner
USER 10001:10001
WORKDIR /workspace
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 HOME=/workspace
CMD ["/bin/sh"]
