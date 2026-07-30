#!/bin/sh
set -eu

: "${CSMS_REMOTE_HOST:?set CSMS_REMOTE_HOST, for example user@server}"

CSMS_REMOTE_PORT="${CSMS_REMOTE_PORT:-22}"
CSMS_REMOTE_DIR="${CSMS_REMOTE_DIR:-/home/${CSMS_REMOTE_HOST%%@*}/csms-platform}"
CSMS_OPERATOR_IMAGE="${CSMS_OPERATOR_IMAGE:-csms-operator:0.1.0}"
CSMS_OPERATOR_ARCHIVE="/tmp/csms-operator-image.tar"
SSH_COMMAND="ssh -p ${CSMS_REMOTE_PORT}"

rsync -az --delete \
	--exclude .git \
	--exclude .idea \
	-e "${SSH_COMMAND}" \
	./ "${CSMS_REMOTE_HOST}:${CSMS_REMOTE_DIR}/"

ssh -p "${CSMS_REMOTE_PORT}" "${CSMS_REMOTE_HOST}" \
	"cd '${CSMS_REMOTE_DIR}' && docker build -f Dockerfile.operator -t '${CSMS_OPERATOR_IMAGE}' . && docker save -o '${CSMS_OPERATOR_ARCHIVE}' '${CSMS_OPERATOR_IMAGE}'"

ssh -t -p "${CSMS_REMOTE_PORT}" "${CSMS_REMOTE_HOST}" \
	"sudo /var/lib/rancher/rke2/bin/ctr --address /run/k3s/containerd/containerd.sock --namespace k8s.io images import '${CSMS_OPERATOR_ARCHIVE}'"

ssh -p "${CSMS_REMOTE_PORT}" "${CSMS_REMOTE_HOST}" \
	"cd '${CSMS_REMOTE_DIR}' &&
	kubectl apply -k config/operator &&
	kubectl rollout status deployment/csms-operator --timeout=120s &&
	kubectl get deployment,pod -l app.kubernetes.io/name=csms-operator -o wide"

echo "CSMS Operator deployment completed"
