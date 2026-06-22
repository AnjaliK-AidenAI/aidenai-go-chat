FROM golang:1.22-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /chat-service .

FROM gcr.io/distroless/static-debian12 AS final
COPY --from=build /chat-service /chat-service
EXPOSE 8080
ENTRYPOINT ["/chat-service"]
