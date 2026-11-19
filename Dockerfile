FROM golang:1.26-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags="-s -w \
      -X github.com/roman/kidstracker/internal/buildinfo.Version=${VERSION} \
      -X github.com/roman/kidstracker/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/roman/kidstracker/internal/buildinfo.BuildTime=${BUILD_TIME}" \
    -o /out/kidstracker ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/kidstracker /app/kidstracker
COPY --from=build /src/web /app/web
EXPOSE 8080
ENTRYPOINT ["/app/kidstracker"]
