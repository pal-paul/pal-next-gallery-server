FROM python:3.11-slim

ARG TORCH_VERSION=2.7.1

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/models/huggingface

WORKDIR /app

COPY embedding/requirements.txt ./requirements.txt
RUN architecture="$(dpkg --print-architecture)" \
    && if [ "${architecture}" = "amd64" ]; then \
        pip install --no-cache-dir "torch==${TORCH_VERSION}" --index-url https://download.pytorch.org/whl/cpu; \
    else \
        pip install --no-cache-dir "torch==${TORCH_VERSION}"; \
    fi \
    && pip install --no-cache-dir -r requirements.txt

COPY embedding/app.py ./app.py

RUN useradd --create-home --uid 10001 embedding \
    && mkdir -p /models/huggingface \
    && chown -R embedding:embedding /models /app

USER embedding

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=10s --start-period=5m --retries=5 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/healthz', timeout=5)" || exit 1

CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "1"]
