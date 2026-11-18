# Both binaries ship in one image: the service, and the one-shot reconciler the
# BDD step `the reconciler runs` executes inside this same container.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/wallet ./cmd/wallet \
 && CGO_ENABLED=0 go build -trimpath -o /out/reconcile ./cmd/reconcile

FROM alpine:3.21
RUN adduser -D -u 10001 wallet
USER wallet
COPY --from=build /out/wallet /usr/local/bin/wallet
COPY --from=build /out/reconcile /usr/local/bin/reconcile
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/wallet"]
