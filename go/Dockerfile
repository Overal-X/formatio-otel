FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /otel-go .

FROM alpine:3.22
COPY --from=build /otel-go /otel-go
EXPOSE 8080
CMD ["/otel-go"]
