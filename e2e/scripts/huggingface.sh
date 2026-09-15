#!/bin/sh
set -e
pip install --no-cache-dir --quiet huggingface_hub >/dev/null 2>&1
python -c "import huggingface_hub as h; print(h.hf_hub_download('bert-base-uncased','config.json'))"
