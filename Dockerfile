# ============================================
# YouTube Automation Studio — Dockerfile
# Multi-stage build: Go binary + FFmpeg + Whisper
# ============================================

# --- Stage 1: Build Go binary ---
FROM golang:1.25-bookworm AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o yt-studio main.go

# --- Stage 2: Runtime ---
FROM python:3.11-slim-bookworm

# Install FFmpeg and system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# Install Whisper for caption generation
RUN python -m pip install --upgrade pip && \
    pip install --no-cache-dir openai-whisper

WORKDIR /app

# Copy Go binary from builder
COPY --from=builder /build/yt-studio .

# Copy frontend UI
COPY ui/ ./ui/

# Create required directories
RUN mkdir -p workspace exports logs storage

# Expose server port
EXPOSE 8000

# Health check
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -qO- http://localhost:8000/api/status || exit 1

# Run the server
CMD ["./yt-studio"]
