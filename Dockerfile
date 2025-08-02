# Dockerfile
FROM golang:1.23-alpine
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o quake3-master main.go
EXPOSE 27950/udp
CMD ["./quake3-master"]
