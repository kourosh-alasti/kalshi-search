FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/kalshi-alerts ./cmd/kalshi-alerts

# Root variant: Railway volumes are mounted root-owned, and the service needs
# to write state files there.
FROM gcr.io/distroless/static-debian12
COPY --from=build /bin/kalshi-alerts /kalshi-alerts
EXPOSE 8080
ENTRYPOINT ["/kalshi-alerts"]
