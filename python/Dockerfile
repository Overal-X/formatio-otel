FROM python:3.13-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
# Formatio's intake speaks OTLP/HTTP and only takes traces. Naming the HTTP exporter keeps the
# SDK off its gRPC default; the other two stop it exporting metrics and logs into a 404.
ENV OTEL_TRACES_EXPORTER=otlp_proto_http \
    OTEL_METRICS_EXPORTER=none \
    OTEL_LOGS_EXPORTER=none \
    PYTHONUNBUFFERED=1
EXPOSE 8080
CMD ["opentelemetry-instrument", "python", "app.py"]
