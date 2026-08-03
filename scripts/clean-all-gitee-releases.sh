#!/usr/bin/env bash
# 删除创建时间超过 3 个月的 Gitee release（保留 git tag）
# 用法: ./clean-all-gitee-releases.sh [--dry-run]

set -euo pipefail

OWNER="xdfield"
REPO="cs-cloud-release"
DRY_RUN=0

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    *) echo "未知参数: $arg" >&2; exit 1 ;;
  esac
done

if [ -z "${GITEE_ACCESS_TOKEN:-}" ]; then
  echo "错误: 请设置环境变量 GITEE_ACCESS_TOKEN" >&2
  echo "示例: export GITEE_ACCESS_TOKEN=your_token_here" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "错误: 需要 jq" >&2
  exit 1
fi

# 3 个月前的时间戳（秒）
cutoff=$(date -u -d '3 months ago' +%s 2>/dev/null \
  || date -u -v-3m +%s 2>/dev/null)
if [ -z "$cutoff" ]; then
  echo "错误: 无法计算截止日期，需要 GNU date 或 BSD date" >&2
  exit 1
fi
cutoff_human=$(date -u -d "@$cutoff" 2>/dev/null || date -u -r "$cutoff" 2>/dev/null)
echo "将清理创建时间早于 ${cutoff_human} (UTC) 的 release"
[ "$DRY_RUN" = "1" ] && echo "[DRY-RUN 模式：仅预览，不会实际删除]"
echo

echo "正在获取所有 releases..."
releases=$(curl -fsS \
  "https://gitee.com/api/v5/repos/${OWNER}/${REPO}/releases?access_token=${GITEE_ACCESS_TOKEN}&per_page=100&page=1")

if [ -z "$releases" ] || [ "$releases" = "[]" ]; then
  echo "没有找到 release"
  exit 0
fi

total=$(printf '%s' "$releases" | jq 'length')
echo "共 ${total} 个 release"
echo

# 输出表头
printf "%-12s %-22s %-26s %s\n" "RELEASE_ID" "CREATED_AT" "TAG" "STATUS"
printf "%-12s %-22s %-26s %s\n" "----------" "----------" "---" "------"

to_delete=()
to_keep=0

while IFS=$'\t' read -r id created_at tag_name; do
  # 解析 created_at 为 UTC 时间戳（兼容 +08:00 / Z 时区）
  ts=$(date -u -d "$created_at" +%s 2>/dev/null \
    || date -u -jf '%Y-%m-%dT%H:%M:%S' "${created_at:0:19}" +%s 2>/dev/null \
    || echo 0)

  if [ "$ts" -lt "$cutoff" ] && [ "$ts" -gt 0 ]; then
    status="DELETE"
    to_delete+=("${id}|${tag_name}")
  else
    status="keep"
    to_keep=$((to_keep + 1))
  fi
  printf "%-12s %-22s %-26s %s\n" "$id" "${created_at:0:19}" "$tag_name" "$status"
done < <(printf '%s' "$releases" | jq -r '.[] | [.id, .created_at, .tag_name] | @tsv')

echo
echo "统计: 保留 ${to_keep}, 待删除 ${#to_delete[@]}"

if [ ${#to_delete[@]} -eq 0 ]; then
  echo "无需清理"
  exit 0
fi

echo
if [ "$DRY_RUN" = "1" ]; then
  echo "[DRY-RUN] 未执行删除。去掉 --dry-run 实际执行。"
  exit 0
fi

read -p "确认删除上述 ${#to_delete[@]} 个 release? [y/N] " confirm
if [ "$confirm" != "y" ] && [ "$confirm" != "Y" ]; then
  echo "取消操作"
  exit 0
fi

echo
deleted=0
failed=0
for entry in "${to_delete[@]}"; do
  id="${entry%%|*}"
  tag="${entry##*|}"
  if curl -fsS -X DELETE \
    "https://gitee.com/api/v5/repos/${OWNER}/${REPO}/releases/${id}?access_token=${GITEE_ACCESS_TOKEN}" \
    > /dev/null 2>&1; then
    echo "  ✓ 删除 release ${id} (${tag})"
    deleted=$((deleted + 1))
    # 简单的速率限制，避免触发 Gitee API 限流
    sleep 1
  else
    echo "  ✗ 删除 release ${id} (${tag}) 失败"
    failed=$((failed + 1))
  fi
done

echo
echo "完成: 成功删除 ${deleted} 个, 失败 ${failed} 个"
echo "注: git tag 仍保留, 如需同步删除请手动执行 git push --delete origin <tag>"
