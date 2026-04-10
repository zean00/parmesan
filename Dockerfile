FROM golang:1.26-alpine AS backend-builder

WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/migrate ./cmd/migrate
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/bootstrap ./cmd/bootstrap

FROM alpine:3.22 AS backend

WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend-builder /out/api /app/api
COPY --from=backend-builder /out/worker /app/worker
COPY --from=backend-builder /out/migrate /app/migrate
COPY --from=backend-builder /out/bootstrap /app/bootstrap
COPY migrations /app/migrations
COPY examples /app/examples
ENV PARMESAN_MIGRATIONS_DIR=/app/migrations

FROM node:22-alpine AS dashboard-builder

WORKDIR /src/dashboard
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci
COPY dashboard ./
RUN npm run build

FROM nginx:1.29-alpine AS dashboard

COPY dashboard/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=dashboard-builder /src/dashboard/dist /usr/share/nginx/html
