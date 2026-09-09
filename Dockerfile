# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src

RUN apk add --no-cache git gcc musl-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/qi-index ./cmd/qi-index

FROM alpine:3.20
RUN apk add --no-cache ca-certificates libgcc && \
    adduser -D -H -u 10001 qi-index
WORKDIR /app

COPY --from=build /out/qi-index /usr/local/bin/qi-index
COPY configs/config.example.yaml /app/configs/config.example.yaml

USER qi-index
EXPOSE 2112
ENTRYPOINT ["qi-index"]
CMD ["follow"]
