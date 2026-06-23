FROM golang:1.22-alpine AS build
WORKDIR /app
ENV GONOSUMDB=* GOFLAGS=-mod=mod GOPROXY=direct
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /chat-service .

FROM gcr.io/distroless/static-debian12 AS final
COPY --from=build /chat-service /chat-service
EXPOSE 8080
ENTRYPOINT ["/chat-service"]
