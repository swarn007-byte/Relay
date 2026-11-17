FROM golang:1.26.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /relay .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /relay /relay
ENV RELAY_PORT=8081 RELAY_DB_PATH=/data/relay.db
EXPOSE 8081
VOLUME ["/data"]
ENTRYPOINT ["/relay"]
