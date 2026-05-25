FROM golang:1.26-alpine AS builder
WORKDIR /app
ENV GOPATH=/go
ENV PATH=$GOPATH/bin:/usr/local/go/bin:$PATH
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o escrowd .

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/escrowd .
CMD ["./escrowd", "both"]
