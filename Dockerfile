# ---- 构建阶段 ----
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/coding-plan-5h-quota-warmer .

# ---- 运行阶段 ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
  && adduser -D -u 10001 warmer \
  && mkdir -p /app/data && chown warmer:warmer /app/data
COPY --from=build /out/coding-plan-5h-quota-warmer /usr/local/bin/coding-plan-5h-quota-warmer
USER warmer
WORKDIR /app
EXPOSE 1688
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:1688/healthz || exit 1
ENTRYPOINT ["coding-plan-5h-quota-warmer"]
CMD ["-config", "/app/config.yaml"]
