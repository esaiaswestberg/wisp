FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags sqlite_fts5 -trimpath -ldflags="-s -w" -o /out/wisp ./cmd/blog

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/wisp /wisp
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/wisp"]
