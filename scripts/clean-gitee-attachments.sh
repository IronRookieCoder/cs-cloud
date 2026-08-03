#!/usr/bin/env bash
# 批量清理 Gitee release 附件
# 用法: ./clean-gitee-attachments.sh <release_id>

set -euo pipefail

if [ $# -lt 1 ]; then
  echo "用法: $0 <release_id>" >&2
  echo "示例: $0 769988" >&2
  exit 1
fi

OWNER="xdfield"
REPO="cs-cloud-release"
RELEASE_ID="$1"

if [ -z "${GITEE_ACCESS_TOKEN:-}" ]; then
  echo "错误: 请设置环境变量 GITEE_ACCESS_TOKEN" >&2
  echo "示例: export GITEE_ACCESS_TOKEN=your_token_here" >&2
  exit 1
fi

echo "正在获取 release ${RELEASE_ID} 的附件列表..."
attachments=$(curl -fsS \
  "https://gitee.com/api/v5/repos/${OWNER}/${REPO}/releases/${RELEASE_ID}/attach_files?access_token=${GITEE_ACCESS_TOKEN}")

if [ -z "$attachments" ] || [ "$attachments" = "[]" ]; then
  echo "没有找到附件"
  exit 0
fi

count=$(printf '%s' "$attachments" | jq 'length')
echo "找到 ${count} 个附件"
echo

printf '%s' "$attachments" | jq -r '.[] | "\(.id)\t\(.name)\t\(.size // 0) bytes"' | while IFS=$'\t' read -r id name size; do
  mb_size=$(awk "BEGIN {printf \"%.2f\", $size/1024/1024}")
  printf "  [%s] %s (%s MB)\n" "$id" "$name" "$mb_size"
done

echo
read -p "确认删除以上所有附件? [y/N] " confirm
if [ "$confirm" != "y" ] && [ "$confirm" != "Y" ]; then
  echo "取消操作"
  exit 0
fi

echo "开始删除..."
deleted=0
failed=0

printf '%s' "$attachments" | jq -r '.[] | .id' | while read -r attach_id; do
  if curl -fsS -X DELETE \
    "https://gitee.com/api/v5/repos/${OWNER}/${REPO}/releases/${RELEASE_ID}/attach_files/${attach_id}?access_token=${GITEE_ACCESS_TOKEN}" \
    > /dev/null 2>&1; then
    echo "  ✓ 删除附件 ${attach_id}"
    deleted=$((deleted + 1))
  else
    echo "  ✗ 删除附件 ${attach_id} 失败"
    failed=$((failed + 1))
  fi
done

echo
echo "完成: 成功删除 ${deleted} 个, 失败 ${failed} 个"
