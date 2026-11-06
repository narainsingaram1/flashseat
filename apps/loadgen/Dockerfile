FROM golang:1.26.1-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/loadgen .

FROM grafana/k6:2.0.0
COPY --from=build /out/loadgen /loadgen
COPY onsale.js /scripts/onsale.js
USER 12345:12345
EXPOSE 8090
ENTRYPOINT ["/loadgen"]
