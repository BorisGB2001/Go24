# Speech synthesis of go24 (GO24DEV-1002): Piper with its HTTP server and the voices go24 offers, downloaded at build
# time so the server works offline.
FROM python:3.12-slim
RUN pip install --no-cache-dir "piper-tts[http]==1.8.0"
WORKDIR /data
RUN for v in ru_RU-irina-medium ru_RU-dmitri-medium ru_RU-denis-medium ru_RU-ruslan-medium en_US-amy-medium en_US-ryan-medium; do \
      python3 -m piper.download_voices --data-dir /data "$v" || exit 1; \
    done
EXPOSE 5000
CMD ["python3", "-m", "piper.http_server", "--host", "0.0.0.0", "--port", "5000", "--data-dir", "/data", "-m", "ru_RU-irina-medium"]
