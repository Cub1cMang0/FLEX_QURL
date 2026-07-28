# ==========================================
# STAGE 1: Builder
# ==========================================
FROM fedora:40 AS builder

# Install build dependencies: C++, CMake, Qt5, and wget (Removed golang from here)
RUN dnf update -y && \
    dnf install -y gcc-c++ cmake qt5-qtbase-devel wget tar && \
    dnf clean all

# Manually install Go 1.26.5
RUN wget https://go.dev/dl/go1.26.5.linux-amd64.tar.gz && \
    rm -rf /usr/local/go && \
    tar -C /usr/local -xzf go1.26.5.linux-amd64.tar.gz
ENV PATH=$PATH:/usr/local/go/bin

# Set the working directory inside the container
WORKDIR /build

# Copy the entire project repository into the container
COPY . .

# Build the C++ CLI
WORKDIR /build/flex-cli
RUN mkdir build && cd build && cmake .. && make

# Build the Go API
WORKDIR /build/flex-webapi
RUN go mod download
RUN go build -o flex-web-api main.go

# ==========================================
# STAGE 2: Runner
# ==========================================
FROM fedora:40

# Install ONLY the runtime dependencies.
# We need qt5-qtbase-gui specifically for the "offscreen" platform plugin.
RUN dnf update -y && \
    dnf install -y qt5-qtbase qt5-qtbase-gui && \
    dnf clean all

WORKDIR /app

# Copy the compiled Go web API from the builder stage
COPY --from=builder /build/flex-webapi/flex-web-api .

# Copy the compiled C++ CLI from the builder stage
# (This ensures it sits right next to the Go binary, matching your cliPath = "./flex-convert-cli")
COPY --from=builder /build/flex-cli/build/flex-convert-cli .

# Copy your frontend static files
COPY --from=builder /build/flex-webapi/static ./static

# Expose port 8080 to the cloud provider
EXPOSE 8080

# Start the Go server
CMD ["./flex-web-api"]