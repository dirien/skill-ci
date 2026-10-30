# Build context: controller/. Built for linux/amd64 by the in-cluster BuildKit.
FROM --platform=linux/amd64 golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /manager ./cmd/manager

FROM gcr.io/distroless/static:nonroot
COPY --from=build /manager /manager
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/manager"]
