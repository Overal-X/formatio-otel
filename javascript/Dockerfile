FROM node:22-alpine
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev
COPY . .
# Intake only takes traces; without the first two the SDK also exports metrics and logs and
# logs a 404 for each. The default detectors also probe cloud metadata endpoints on start.
ENV OTEL_METRICS_EXPORTER=none \
    OTEL_LOGS_EXPORTER=none \
    OTEL_NODE_RESOURCE_DETECTORS=env,host,os,process,serviceinstance,container \
    NODE_ENV=production
EXPOSE 8080
# Not `npm start`: npm would take the signals and log every stop as a failed command.
CMD ["node", "--require", "@opentelemetry/auto-instrumentations-node/register", "app.js"]
