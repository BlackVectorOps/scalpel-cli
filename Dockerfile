# -- Stage 1: Build the binary --
# Use the official Go image, ensuring the version matches our go.mod file.
FROM golang:1.22-alpine AS builder

# Install CA certificates (necessary for the binary if it makes HTTPS calls, and for the final stage)
RUN apk add --no-cache ca-certificates

# Define a build argument for the version, with a default value.
ARG VERSION=development

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum first to leverage Docker layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy the entire source code into the container.
COPY . .

# Build the Go application into a single, statically linked binary.
# CGO_ENABLED=0 creates a static binary without C dependencies.
# GOOS=linux ensures it's built for the Alpine Linux environment.
# -ldflags is used to inject the version number into the cmd.Version variable.
# The path must match the package structure: github.com/xkilldash9x/scalpel-cli/cmd.Version
# The build target is ./cmd/scalpel/main.go based on the project structure.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-X 'github.com/xkilldash9x/scalpel-cli/cmd.Version=$VERSION'" \
    -a -o /usr/local/bin/scalpel-cli ./cmd/scalpel/main.go

# -- Stage 2: Create the final, minimal image --
# Start from a scratch image for a minimal footprint.
FROM scratch

# Copy essential CA certificates from the builder stage (required for HTTPS calls)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy only the compiled binary from the builder stage.
COPY --from=builder /usr/local/bin/scalpel-cli /usr/local/bin/scalpel-cli

# Define the command to run when the container starts.
ENTRYPOINT ["/usr/local/bin/scalpel-cli"]
