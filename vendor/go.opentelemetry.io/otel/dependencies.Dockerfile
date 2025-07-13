# This is a renovate-friendly source of Docker images.
FROM python:3.13.5-slim-bullseye@sha256:631af3fee9d0b0a046855a62af745c1f94b75c5309be8802a0928cce3ac0f98d AS python
FROM otel/weaver:v0.13.2@sha256:ae7346b992e477f629ea327e0979e8a416a97f7956ab1f7e95ac1f44edf1a893 AS weaver
