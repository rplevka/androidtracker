FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kidstracker ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/kidstracker /app/kidstracker
COPY --from=build /src/web /app/web
EXPOSE 8080
ENTRYPOINT ["/app/kidstracker"]
