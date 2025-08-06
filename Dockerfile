FROM golang:1.24.3-alpine AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies
RUN go mod download

# Copy the source code into the container
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -o /gerbil

# Start a new stage from scratch
FROM ubuntu:24.04 AS runner

# Install Tailscale and dependencies
RUN apt-get update && \
    apt-get install -y curl gnupg lsb-release iptables iproute2 && \
    curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg | tee /usr/share/keyrings/tailscale-archive-keyring.gpg >/dev/null && \
    curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list | tee /etc/apt/sources.list.d/tailscale.list && \
    apt-get update && \
    apt-get install -y tailscale && \
    rm -rf /var/lib/apt/lists/*

# Copy the pre-built binary file from the previous stage and the entrypoint script
COPY --from=builder /gerbil /usr/local/bin/
COPY entrypoint.sh /

RUN chmod +x /entrypoint.sh

# Create state directory for Tailscale
RUN mkdir -p /var/lib/tailscale

# Copy the entrypoint script
ENTRYPOINT ["/entrypoint.sh"]

# Command to run the executable
CMD ["gerbil"]