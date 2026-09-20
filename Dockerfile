# Not needed for Render, which builds Go natively. It is here so the same
# service can run anywhere that takes a container.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /battlesnake ./cmd/battlesnake

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /battlesnake /battlesnake
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/battlesnake"]
