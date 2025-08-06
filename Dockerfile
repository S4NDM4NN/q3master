# # Dockerfile
# FROM golang:1.23-alpine
# WORKDIR /app
# COPY go.mod go.sum ./
# RUN go mod download
# COPY . .
# RUN go build -o quake3-master main.go
# EXPOSE 27950/udp
# CMD ["./quake3-master"]
FROM golang:1.23-alpine

WORKDIR /app

# Copy go.mod and go.sum first for caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the entire project
RUN go build -o q3master .

# Expose HTTP and UDP ports
EXPOSE 8080
EXPOSE 27950/udp

CMD ["./q3master"]
