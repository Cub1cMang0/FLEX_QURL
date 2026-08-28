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

WORKDIR /build/flex-image-viewer
RUN go mod download
RUN go build -o flex-image-viewer main.go

# ==========================================
# STAGE 2: qURL CLI
# ==========================================
FROM debian:bookworm-slim AS qurl

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates jq \
    && rm -rf /var/lib/apt/lists/*

ARG QURL_VERSION=2.0.3
RUN curl -fsSL "https://github.com/layervai/qurl-integrations/releases/download/v${QURL_VERSION}/qurl_${QURL_VERSION}_linux_amd64.tar.gz" -o /tmp/qurl.tar.gz \
    && tar -xzf /tmp/qurl.tar.gz -C /usr/local/bin qurl \
    && rm /tmp/qurl.tar.gz \
    && chmod +x /usr/local/bin/qurl

# ==========================================
# STAGE 3: Runner
# ==========================================
FROM fedora:40

# Install ONLY the runtime dependencies.
# We need qt5-qtbase-gui specifically for the "offscreen" platform plugin.
RUN dnf update -y && \
    dnf install -y qt5-qtbase qt5-qtbase-gui && \
    dnf clean all

COPY --from=qurl /usr/local/bin/qurl /usr/local/bin/qurl

WORKDIR /app

# Copy the compiled Go web API from the builder stage
COPY --from=builder /build/flex-webapi/flex-web-api .
COPY --from=builder /build/flex-image-viewer/flex-image-viewer .

# Copy the compiled C++ CLI from the builder stage
# (This ensures it sits right next to the Go binary, matching your cliPath = "./flex-convert-cli")
COPY --from=builder /build/flex-cli/build/flex-convert-cli .

# Copy your frontend static files
COPY --from=builder /build/flex-webapi/static ./static

# Ensure persistent job store directory

# Expose port 8080 and 8081 to the cloud provider
EXPOSE 8080 8081

# Create miniscript to start both services
RUN echo -e '#!/bin/sh\n./flex-image-viewer &\n./flex-web-api' > start.sh && \
    chmod +x start.sh

# Start the Go server
CMD ["./start.sh"]