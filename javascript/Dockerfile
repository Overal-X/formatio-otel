FROM node:22-alpine
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY . .
# Intake only takes traces; without these the SDK also exports metrics and logs and logs a
# 404 for each.
ENV OTEL_METRICS_EXPORTER=none \
    OTEL_LOGS_EXPORTER=none \
    NODE_ENV=production
EXPOSE 8080
CMD ["npm", "start"]
