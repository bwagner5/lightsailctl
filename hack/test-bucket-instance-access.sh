#!/usr/bin/env bash
# test-bucket-instance-access.sh
#
# Verifies that `SetResourceAccessForBucket(allow)` lets a Lightsail instance
# read+write a Lightsail bucket using only IMDS-delivered credentials (no keys).
#
# If this works, we can drop the /opt/lightsail/<app>/<env>/.credentials file
# and have the on-instance watcher authenticate purely through IMDS.
#
# Usage:
#   ./test-bucket-instance-access.sh <instance-name> [region]
#
# Requires:
#   - aws CLI configured locally with Lightsail permissions
#   - The named instance exists and accepts SSH via `aws lightsail get-instance-access-details`
#   - jq

set -euo pipefail

INSTANCE="${1:-}"
REGION="${2:-${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}}"

if [[ -z "$INSTANCE" ]]; then
  echo "usage: $0 <instance-name> [region]" >&2
  exit 2
fi

command -v jq >/dev/null || { echo "jq required"; exit 2; }
command -v aws >/dev/null || { echo "aws CLI required"; exit 2; }

ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
SUFFIX=$(date +%s)
BUCKET="ls-test-${ACCOUNT}-${SUFFIX}"

trap 'cleanup' EXIT
cleanup() {
  echo
  echo "=== CLEANUP ==="
  aws lightsail set-resource-access-for-bucket \
    --region "$REGION" \
    --resource-name "$INSTANCE" \
    --bucket-name "$BUCKET" \
    --access deny 2>/dev/null || true
  aws lightsail delete-bucket \
    --region "$REGION" \
    --bucket-name "$BUCKET" \
    --force-delete 2>/dev/null || true
  [[ -n "${TMPKEY:-}" ]] && rm -f "$TMPKEY" "${TMPKEY}-cert.pub"
  echo "cleanup done"
}

echo "=== SETUP ==="
echo "instance : $INSTANCE"
echo "region   : $REGION"
echo "bucket   : $BUCKET"
echo

echo "1) creating bucket"
aws lightsail create-bucket \
  --region "$REGION" \
  --bucket-name "$BUCKET" \
  --bundle-id small_1_0 >/dev/null

echo "   waiting for bucket to be Ready ..."
for i in {1..30}; do
  STATE=$(aws lightsail get-buckets --region "$REGION" --bucket-name "$BUCKET" \
    --query 'buckets[0].state.code' --output text 2>/dev/null || echo "missing")
  [[ "$STATE" == "OK" || "$STATE" == "Active" ]] && break
  sleep 2
done
echo "   bucket state: $STATE"

sleep 60

echo
echo "2) granting instance '$INSTANCE' access to bucket"
aws lightsail set-resource-access-for-bucket \
  --region "$REGION" \
  --resource-name "$INSTANCE" \
  --bucket-name "$BUCKET" \
  --access allow
echo "   waiting 5s for propagation"
sleep 60

echo
echo "3) fetching SSH credentials for $INSTANCE"
ACCESS=$(aws lightsail get-instance-access-details \
  --region "$REGION" \
  --instance-name "$INSTANCE" \
  --protocol ssh)

TMPKEY=$(mktemp "${TMPDIR:-/tmp}/ls-ssh-XXXXXX")
echo "$ACCESS" | jq -r '.accessDetails.privateKey' > "$TMPKEY"
chmod 600 "$TMPKEY"
CERT=$(echo "$ACCESS" | jq -r '.accessDetails.certKey // empty')
if [[ -n "$CERT" ]]; then
  echo "$CERT" > "${TMPKEY}-cert.pub"
  chmod 600 "${TMPKEY}-cert.pub"
fi
HOST=$(echo "$ACCESS" | jq -r '.accessDetails.ipAddress')
USER=$(echo "$ACCESS" | jq -r '.accessDetails.username')

SSH_OPTS=(-i "$TMPKEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=10)
SSH_TARGET="${USER}@${HOST}"

echo
echo "4) remote test: can instance read/write \"$BUCKET\" with NO creds in env?"
REMOTE_SCRIPT=$(cat <<REMOTE
set -e
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
export AWS_DEFAULT_REGION=$REGION

echo "   -- aws sts get-caller-identity (via IMDS):"
aws sts get-caller-identity 2>&1 | sed 's/^/      /'

echo
echo "   -- aws s3 ls s3://$BUCKET:"
aws s3 ls s3://$BUCKET 2>&1 | sed 's/^/      /' || echo "      (empty or failed)"

echo
echo "   -- aws s3 cp (upload test file):"
echo "hello from instance" > /tmp/ls-test.txt
if aws s3 cp /tmp/ls-test.txt s3://$BUCKET/test.txt 2>&1 | sed 's/^/      /'; then
  echo "   UPLOAD OK"
else
  echo "   UPLOAD FAILED"
  exit 1
fi

echo
echo "   -- aws s3 cp (download test file):"
if aws s3 cp s3://$BUCKET/test.txt /tmp/ls-test-back.txt 2>&1 | sed 's/^/      /'; then
  echo "   DOWNLOAD OK"
else
  echo "   DOWNLOAD FAILED"
  exit 1
fi

rm -f /tmp/ls-test.txt /tmp/ls-test-back.txt
echo
echo "   ALL REMOTE OPS SUCCEEDED"
REMOTE
)

if ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "$REMOTE_SCRIPT"; then
  echo
  echo "=== RESULT: PASS ==="
  echo "SetResourceAccessForBucket grants bucket access via IMDS."
  echo "We can drop the /opt/lightsail/<app>/<env>/.credentials file."
else
  echo
  echo "=== RESULT: FAIL ==="
  echo "Instance could not access bucket without env credentials."
  echo "We need to continue writing .credentials onto the instance."
  exit 1
fi
