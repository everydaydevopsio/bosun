FROM ghcr.io/orchael/bridgectl:latest

USER root
RUN apt-get update && apt-get install -y --no-install-recommends git python3 python3-pip && rm -rf /var/lib/apt/lists/*
WORKDIR /app/bosun
COPY requirements.txt .
RUN pip3 install --break-system-packages --no-cache-dir -r requirements.txt
COPY bosun ./bosun
COPY prompts ./prompts
ENV PYTHONPATH=/app/bosun
ENTRYPOINT []
CMD ["python3", "-m", "uvicorn", "bosun.app:app", "--host", "0.0.0.0", "--port", "8080"]
