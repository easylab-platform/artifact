#!/bin/sh
set -e
helm repo add easylab https://charts.helm.sh/stable >/dev/null
helm repo update >/dev/null 2>&1
helm pull easylab/redis -d /tmp
