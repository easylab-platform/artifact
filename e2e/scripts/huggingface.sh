#!/bin/sh
set -e
pip install --no-cache-dir --quiet huggingface_hub >/dev/null 2>&1
# Run assertion: fetch the file and parse it as JSON.
python - <<'PY'
import json, huggingface_hub as h
p = h.hf_hub_download('bert-base-uncased', 'config.json')
cfg = json.load(open(p))
print("huggingface: downloaded", cfg.get("model_type"), "config")
PY
