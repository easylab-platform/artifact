#!/bin/sh
# Jenkins update center (updates.jenkins.io): plugin index + a plugin hpi.
set -e
CA=/etc/easysidecar/ca.crt
W="wget --ca-certificate=$CA -qO"
$W /tmp/uc.json "https://updates.jenkins.io/update-center.json"
grep -q 'updateCenterVersion' /tmp/uc.json || { echo "jenkins: bad update-center"; exit 1; }
wget --ca-certificate=$CA -qO /tmp/git.hpi "https://updates.jenkins.io/download/plugins/git/latest/git.hpi"
[ -s /tmp/git.hpi ] || { echo "jenkins: empty hpi"; exit 1; }
echo "jenkins: update-center + git plugin ok"
