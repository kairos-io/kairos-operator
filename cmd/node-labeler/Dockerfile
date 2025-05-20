# Build stage
FROM golang:1.24.3-alpine AS builder

WORKDIR /workspace
COPY . .

RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o node-labeler ./cmd/node-labeler

# Final stage
FROM alpine:3.19

COPY --from=builder /workspace/node-labeler /usr/local/bin/node-labeler

ENTRYPOINT ["/usr/local/bin/node-labeler"] 