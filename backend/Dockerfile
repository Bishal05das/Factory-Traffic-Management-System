FROM golang:1.24.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates wget && addgroup -S traffic && adduser -S traffic -G traffic
COPY --from=build /out/server /usr/local/bin/server
USER traffic
EXPOSE 8080
CMD ["server"]
