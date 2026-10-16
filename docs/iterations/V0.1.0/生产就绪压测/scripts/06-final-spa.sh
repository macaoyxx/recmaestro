#!/usr/bin/env bash
# 终验：SPA 资产抽查 —— index.html + 2 个 JS chunk（从 index.html 引用中取）
set -u
BASE=http://127.0.0.1:8080

echo "=== [1] GET / (index.html) ==="
curl -s -o /tmp/fv-index.html -w 'HTTP:%{http_code} type:%{content_type} size:%{size_download}\n' "$BASE/"
echo "--- index.html 前 12 行 ---"
head -12 /tmp/fv-index.html

echo "=== [2] 从 index.html 提取 chunk 引用 ==="
chunks=$(grep -oP '(src|href)="/assets/[^"]+"' /tmp/fv-index.html | grep -oP '/assets/[^"]+' | sort -u)
echo "$chunks"
c1=$(echo "$chunks" | sed -n '1p')
c2=$(echo "$chunks" | sed -n '2p')
if [ -z "$c1" ] || [ -z "$c2" ]; then echo "[FAIL] index.html 中未找到 >=2 个 /assets/ 引用"; exit 1; fi

for c in "$c1" "$c2"; do
  echo "=== [3] GET $c ==="
  curl -s -o /dev/null -w "HTTP:%{http_code} type:%{content_type} size:%{size_download}\n" "$BASE$c"
done
