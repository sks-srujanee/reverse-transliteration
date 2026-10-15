FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /reverse-api .

FROM alpine:3.23
RUN addgroup -S api && adduser -S -G api api && mkdir /cache && chown api:api /cache
WORKDIR /app
COPY --from=build /reverse-api ./reverse-api
COPY --chown=api:api --chmod=755 bin/translit-reverse-linux-amd64 ./bin/translit-reverse
ENV HOST=0.0.0.0 PORT=4100 XDG_CACHE_HOME=/cache
USER api
EXPOSE 4100
HEALTHCHECK --start-period=30s --interval=30s --timeout=3s CMD ["/app/reverse-api", "healthcheck"]
CMD ["/app/reverse-api"]
