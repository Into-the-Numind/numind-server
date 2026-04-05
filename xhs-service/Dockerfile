FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY main.py .

EXPOSE 8100

# XHS_COOKIE must be provided as environment variable (JSON format)
# Example: XHS_COOKIE='{"web_session":"xxx","a1":"xxx"}'
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8100"]
