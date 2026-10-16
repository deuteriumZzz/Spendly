FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/spendly ./cmd/spendly

# distroless: no shell, CA certificates included, runs as non-root
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/spendly /spendly
EXPOSE 8080
ENTRYPOINT ["/spendly"]
CMD ["serve"]
