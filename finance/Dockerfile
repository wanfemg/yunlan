FROM python:3.12-slim

LABEL org.opencontainers.image.title="yunlan-finance" \
    org.opencontainers.image.description="Integrated finance service for yunlan" \
    org.opencontainers.image.source="https://github.com/wanfemg/yunlan"

WORKDIR /app/finance
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 DATA_DIR=/app/data
COPY finance/requirements.txt ./
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && pip install --no-cache-dir -r requirements.txt \
    && groupadd --gid 10001 app \
    && useradd --uid 10001 --gid 10001 --no-create-home app \
    && mkdir -p /app/data \
    && chown app:app /app/data
COPY --chown=app:app finance/app/ ./app/
COPY --chown=app:app finance/web/ ./web/
COPY --chown=app:app finance/demo.py finance/run.py ./
COPY --chown=app:app LICENSE THIRD_PARTY_NOTICES.md /app/
USER app:app
ENTRYPOINT ["python", "/app/finance/run.py"]
