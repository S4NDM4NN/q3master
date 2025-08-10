FROM golang:1.23-alpine

WORKDIR /app

# Copy go.mod and go.sum first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the entire project
RUN go build -o q3master ./cmd/q3master

# Expose HTTP and UDP ports
EXPOSE 8080

CMD ["./q3master"]
